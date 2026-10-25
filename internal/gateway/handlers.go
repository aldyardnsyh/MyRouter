package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/store"
)

// Handler returns the /v1 router.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", g.handleModels)
	mux.HandleFunc("GET /models", g.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", g.handleChat)
	mux.HandleFunc("POST /chat/completions", g.handleChat)
	return mux
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	auth, err := g.Authenticate(r)
	if err != nil {
		WriteOpenAIError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", err.Error(), 0)
		return
	}
	_ = g.db.TouchAPIKey(auth.Key.ID, store.Now())
	models, err := g.ListModels(r.Context())
	if err != nil {
		WriteOpenAIError(w, http.StatusInternalServerError, "server_error", "internal", "failed to list models", 0)
		return
	}
	routes, _ := g.db.ListRoutes()
	allowed := map[string]bool{}
	for _, a := range store.SplitCSV(auth.Key.AllowedRoutes) {
		allowed[strings.ToLower(a)] = true
	}
	data := make([]OpenAIModel, 0, len(models))
	if len(allowed) == 0 {
		data = models
	} else {
		for _, m := range models {
			if allowed[strings.ToLower(m.ID)] {
				data = append(data, m)
			}
		}
	}
	_ = routes
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	requestID := store.RandomHex(8)
	w.Header().Set("X-Request-Id", requestID)
	started := time.Now()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		WriteOpenAIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "payload_too_large", "request body exceeds 8 MB", 0)
		return
	}

	auth, err := g.Authenticate(r)
	if err != nil {
		g.logActivity(requestID, started, r, nil, nil, nil, "blocked", http.StatusUnauthorized, 0, 0, 0, "", err.Error(), nil)
		WriteOpenAIError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", err.Error(), 0)
		return
	}

	input, err := ParseChat(body)
	if err != nil {
		WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error(), 0)
		return
	}

	route, err := g.db.RouteByAlias(input.Model)
	if err != nil {
		g.logActivity(requestID, started, r, auth, nil, nil, "blocked", http.StatusNotFound, 0, 0, 0, "", "unknown route alias: "+input.Model, nil)
		WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "model_not_found",
			fmt.Sprintf("route alias %q not found", input.Model), 0)
		return
	}
	if allowed := store.SplitCSV(auth.Key.AllowedRoutes); len(allowed) > 0 && !containsFold(allowed, route.Alias) {
		g.logActivity(requestID, started, r, auth, route, nil, "blocked", http.StatusForbidden, 0, 0, 0, "api_key", "route not permitted for key", nil)
		WriteOpenAIError(w, http.StatusForbidden, "invalid_request_error", "route_forbidden",
			fmt.Sprintf("route %q is not allowed for this key", route.Alias), 0)
		return
	}

	// Device identification (soft).
	ip := auth.IP
	ua := r.UserAgent()
	fp := guard.Fingerprint(auth.Key.ID, ip, ua, r.Header.Get("X-Device-Id"))
	deviceID, _ := g.db.UpsertDevice(auth.Key.ID, fp, ip, ua)
	if blocked, _ := g.db.IsDeviceBlocked(auth.Key.ID, fp); blocked {
		g.logActivity(requestID, started, r, auth, route, nil, "blocked", http.StatusForbidden, 0, 0, 0, "device", "device is blocked", nil)
		WriteOpenAIError(w, http.StatusForbidden, "invalid_request_error", "device_blocked", "this device is blocked by Guard", 0)
		return
	}

	prep := &PreparedRequest{
		RequestID: requestID, Input: input, Route: route, APIKey: auth.Key, IP: ip,
		DeviceID: deviceID, DeviceFP: fp, UserAgent: ua,
		EstTokens: EstimateTokens(input.Messages) + 256, Started: started,
	}

	// Guard evaluation against the primary provider scope first.
	var primaryProv *model.Provider
	if pt, ok := route.Primary(); ok {
		primaryProv, _ = g.db.ProviderByID(pt.ProviderID)
	}
	guardCtx := context.WithValue(r.Context(), struct{}{}, true)
	decision := g.guard.Check(guardCtx, prep.GuardRequest(primaryProv))
	// on_exceed=queue: wait for the bucket to refill, bounded by the policy's
	// queue_max_s. Without a positive queue_max_s we keep the reject behavior.
	if !decision.Allowed && decision.OnExceed == "queue" &&
		decision.HTTPStatus == http.StatusTooManyRequests && decision.QueueMaxS > 0 {
		deadline := time.Now().Add(time.Duration(decision.QueueMaxS) * time.Second)
		for !decision.Allowed {
			wait := decision.RetryAfter
			if wait <= 0 {
				wait = 1
			}
			if wait > decision.QueueMaxS {
				wait = decision.QueueMaxS
			}
			if time.Now().Add(time.Duration(wait) * time.Second).After(deadline) {
				break
			}
			select {
			case <-time.After(time.Duration(wait) * time.Second):
			case <-r.Context().Done():
				return
			}
			decision = g.guard.Check(guardCtx, prep.GuardRequest(primaryProv))
		}
	}
	if !decision.Allowed {
		g.logActivity(requestID, started, r, auth, route, primaryProv, "blocked", decision.HTTPStatus, 0, 0, 0,
			guardScope(decision.Scope, decision.PolicyName), decision.Reason, []model.TraceStep{{
				Attempt: 0, Target: input.Model, Outcome: "guard_blocked", Category: decision.Scope,
				StatusCode: decision.HTTPStatus, Message: decision.Reason,
			}})
		code := "rate_limit_exceeded"
		if decision.HTTPStatus == http.StatusForbidden {
			code = "forbidden"
		}
		WriteOpenAIError(w, decision.HTTPStatus, "rate_limit_exceeded", code,
			fmt.Sprintf("%s (%s scope: %s)", decision.Reason, decision.Scope, decision.PolicyName), decision.RetryAfter)
		return
	}

	// Provider-level Guard: re-check with every target provider so a provider
	// quota cannot be exceeded via fallback.
	release := g.guard.Acquire(prep.GuardRequest(primaryProv))
	defer release()

	// Live telemetry for the dashboard routing map.
	liveID := requestID
	g.live.start(liveID, route.Alias, input.Model, auth.Key.Prefix, ip,
		idOfProvider(primaryProv), provName(primaryProv))
	defer g.live.finish(liveID, "error")

	outcome, execErr := g.execute(r.Context(), prep)
	if execErr != nil {
		g.live.finish(liveID, "error")
		g.logActivity(requestID, started, r, auth, route, primaryProv, "blocked", http.StatusForbidden, 0, 0, 0, "", execErr.Error(), nil)
		// Do not echo internal error text (may contain upstream details).
		WriteOpenAIError(w, http.StatusForbidden, "invalid_request_error", "route_unavailable", "route is not available", 0)
		return
	}
	g.live.setProvider(liveID, idOfProvider(outcome.Provider), provName(outcome.Provider))

	latency := time.Since(started)
	if outcome.Err != nil && !outcome.StreamStarted {
		g.live.finish(liveID, "error")
		status := statusFor(outcome.Err)
		g.logActivity(requestID, started, r, auth, route, outcome.Provider, "error", status, latency.Milliseconds(),
			outcome.InTokens, outcome.OutTokens, "", outcome.Err.Message, outcome.Trace)
		WriteOpenAIError(w, status, "upstream_error", string(outcome.Err.Category), outcome.Err.Message, 0)
		return
	}

	if outcome.StreamStarted {
		streamStatus := g.streamResponse(w, r, prep, outcome, requestID, started)
		g.live.finish(liveID, streamStatus)
		return
	}

	// Non-streaming success.
	resp := outcome.Response
	tokens := resp.Usage.InputTokens + resp.Usage.OutputTokens
	g.guard.AddTokens(prep.GuardRequest(outcome.Provider), tokens)
	if err := g.db.TouchAPIKey(auth.Key.ID, store.Now()); err != nil {
		g.log.Debug("touch api key failed", logx.F("error", err.Error()))
	}
	status := "ok"
	fallbackFrom := ""
	if outcome.Fallback {
		status = "ok"
		fallbackFrom = input.Model
	}
	g.logActivity(requestID, started, r, auth, route, outcome.Provider, status, http.StatusOK,
		latency.Milliseconds(), resp.Usage.InputTokens, resp.Usage.OutputTokens, fallbackFrom, "", outcome.Trace)
	g.recordUsage(route, outcome.Provider, auth.Key, status, resp.Usage, outcome.Fallback, outcome.Model)
	g.live.finish(liveID, "ok")

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-" + requestID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   input.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": resp.Content},
			"finish_reason": defaultFinish(resp.Finish),
		}},
		"usage": map[string]any{
			"prompt_tokens":     resp.Usage.InputTokens,
			"completion_tokens": resp.Usage.OutputTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		},
	})
}

// streamResponse proxies a stream, sending fallback only before the first byte.
// It returns the terminal status for live telemetry ("ok" or "error").
func (g *Gateway) streamResponse(w http.ResponseWriter, r *http.Request, prep *PreparedRequest, outcome *Outcome, requestID string, started time.Time) string {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	inTok, outTok := 0, 0
	firstChunk := true
	// writeFailed records a broken client connection so the loop can stop.
	writeFailed := false
	writeChunk := func(delta string, finish string) {
		payload := map[string]any{
			"id":      "chatcmpl-" + requestID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   prep.Input.Model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{"content": delta},
				"finish_reason": nil,
			}},
		}
		if finish != "" {
			payload["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": defaultFinish(finish)}}
		}
		buf, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", buf); err != nil {
			writeFailed = true
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	for ev := range outcome.Stream {
		// Stop draining the upstream as soon as the client goes away so we do
		// not keep consuming provider quota for an abandoned request.
		if r.Context().Err() != nil {
			g.finishStream(prep, outcome, requestID, started, inTok, outTok, "error", "client disconnected")
			return "error"
		}
		if ev.Err != nil {
			// After the stream has started we cannot fail over: emit an error
			// event and stop (PRD §8).
			errBuf, _ := json.Marshal(map[string]any{"error": map[string]any{
				"message": ev.Err.Error(), "type": "upstream_error", "code": "stream_interrupted",
			}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", errBuf)
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			g.finishStream(prep, outcome, requestID, started, inTok, outTok, "error", "stream interrupted")
			return "error"
		}
		if ev.Delta != "" {
			if firstChunk {
				firstChunk = false
			}
			writeChunk(ev.Delta, "")
			if writeFailed {
				g.finishStream(prep, outcome, requestID, started, inTok, outTok, "error", "client write failed")
				return "error"
			}
		}
		if ev.InputTok > 0 {
			inTok = ev.InputTok
		}
		if ev.OutputTok > 0 {
			outTok = ev.OutputTok
		}
		if ev.Finish != "" {
			writeChunk("", ev.Finish)
			if writeFailed {
				g.finishStream(prep, outcome, requestID, started, inTok, outTok, "error", "client write failed")
				return "error"
			}
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	g.finishStream(prep, outcome, requestID, started, inTok, outTok, "ok", "")
	return "ok"
}

func (g *Gateway) finishStream(prep *PreparedRequest, outcome *Outcome, requestID string, started time.Time, inTok, outTok int, status, errMsg string) {
	if inTok == 0 {
		inTok = prep.EstTokens
	}
	g.guard.AddTokens(prep.GuardRequest(outcome.Provider), inTok+outTok)
	fallbackFrom := ""
	if outcome.Fallback {
		fallbackFrom = prep.Input.Model
	}
	_ = g.db.TouchAPIKey(prep.APIKey.ID, store.Now())
	entry := &model.RequestLog{
		RequestID: requestID, TS: started.Unix(), RouteID: prep.Route.ID, RequestedModel: prep.Input.Model,
		ModelID: idOf(outcome.Model), ProviderID: idOfProvider(outcome.Provider), APIKeyID: prep.APIKey.ID,
		IP: prep.IP, DeviceID: prep.DeviceID, Status: status, HTTPStatus: http.StatusOK,
		LatencyMS: time.Since(started).Milliseconds(), InTokens: inTok, OutTokens: outTok,
		FallbackFrom: fallbackFrom, ErrorCode: errMsg, Trace: outcome.Trace,
	}
	_ = g.db.InsertRequestLog(entry)
	_ = g.db.AddUsage(time.Now().Format("2006-01-02"), prep.Route.ID, idOfProvider(outcome.Provider), prep.APIKey.ID,
		status, inTok, outTok, outcome.Fallback, g.costOf(outcome.Model, inTok, outTok))
}

func (g *Gateway) logActivity(requestID string, started time.Time, r *http.Request, auth *AuthResult, route *model.Route, prov *model.Provider,
	status string, httpStatus int, latency int64, inTok, outTok int, guardReason, errMsg string, trace []model.TraceStep) {
	entry := &model.RequestLog{
		RequestID: requestID, TS: started.Unix(), Status: status, HTTPStatus: httpStatus,
		LatencyMS: latency, InTokens: inTok, OutTokens: outTok, GuardReason: guardReason, ErrorCode: errMsg, Trace: trace,
	}
	if entry.Trace == nil {
		entry.Trace = []model.TraceStep{}
	}
	if auth != nil {
		entry.APIKeyID = auth.Key.ID
		entry.IP = auth.IP
	}
	if route != nil {
		entry.RouteID = route.ID
		entry.RequestedModel = route.Alias
	}
	if prov != nil {
		entry.ProviderID = prov.ID
	}
	_ = g.db.InsertRequestLog(entry)
	if route != nil {
		_ = g.db.AddUsage(time.Now().Format("2006-01-02"), route.ID, entry.ProviderID, entry.APIKeyID, status, inTok, outTok, false, nil)
	}
}

func (g *Gateway) recordUsage(route *model.Route, prov *model.Provider, key *model.APIKey, status string, usage provider.Usage, fallback bool, mdl *model.Model) {
	_ = g.db.AddUsage(time.Now().Format("2006-01-02"), route.ID, idOfProvider(prov), key.ID, status,
		usage.InputTokens, usage.OutputTokens, fallback, g.costOf(mdl, usage.InputTokens, usage.OutputTokens))
}

// costOf computes an estimated cost using per-1M-token prices when available.
func (g *Gateway) costOf(mdl *model.Model, inTok, outTok int) *float64 {
	if mdl == nil || (mdl.PriceIn == nil && mdl.PriceOut == nil) {
		return nil
	}
	var cost float64
	if mdl.PriceIn != nil {
		cost += float64(inTok) * (*mdl.PriceIn) / 1_000_000
	}
	if mdl.PriceOut != nil {
		cost += float64(outTok) * (*mdl.PriceOut) / 1_000_000
	}
	return &cost
}

func after(_ []model.TraceStep, _ *model.Model, _ *model.Provider) int { return 0 }

func statusFor(e *provider.Error) int {
	switch e.Category {
	case provider.CatRateLimit:
		return http.StatusTooManyRequests
	case provider.CatTimeOut:
		return http.StatusGatewayTimeout
	case provider.CatAuth:
		return http.StatusBadGateway
	case provider.CatInvalidRequest:
		return http.StatusBadRequest
	case provider.CatNotFound:
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}

func guardScope(scope, policy string) string {
	if policy == "" {
		return scope
	}
	return scope + ":" + policy
}

func defaultFinish(s string) string {
	switch s {
	case "", "stop", "end_turn", "STOP":
		return "stop"
	case "length", "max_tokens", "MAX_TOKENS":
		return "length"
	}
	return s
}

func idOf(m *model.Model) string {
	if m == nil {
		return ""
	}
	return m.ID
}

func idOfProvider(p *model.Provider) string {
	if p == nil {
		return ""
	}
	return p.ID
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}
