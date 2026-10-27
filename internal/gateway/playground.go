package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/store"
)

// PlaygroundRequest is an admin-initiated test call. It runs through the same
// routing/fallback pipeline as a real gateway request, but with a synthetic
// API key so it does not consume a client key's quota. Provider-level Guard
// limits still apply, and tokens are still counted against the provider.
type PlaygroundRequest struct {
	Route       string
	System      string
	Messages    []provider.ChatMessage
	MaxTokens   int
	Temperature *float64
	Stream      bool
}

// PlaygroundResult is the normalized outcome shown in the Test Lab.
type PlaygroundResult struct {
	Route      string            `json:"route"`
	RequestID  string            `json:"request_id"`
	Status     string            `json:"status"` // ok|error|blocked
	Provider   string            `json:"provider"`
	ProviderID string            `json:"provider_id"`
	Model      string            `json:"model"`
	ModelID    string            `json:"model_id"`
	Content    string            `json:"content"`
	LatencyMS  int64             `json:"latency_ms"`
	TTFTMS     int64             `json:"ttft_ms"`
	InTokens   int               `json:"in_tokens"`
	OutTokens  int               `json:"out_tokens"`
	Fallback   bool              `json:"fallback"`
	Category   string            `json:"category,omitempty"`
	Error      string            `json:"error,omitempty"`
	Trace      []model.TraceStep `json:"trace"`
}

// PlaygroundEvent is one SSE frame emitted while streaming a test call.
type PlaygroundEvent struct {
	Type   string            `json:"type"` // start|delta|error|done
	Text   string            `json:"text,omitempty"`
	Error  string            `json:"error,omitempty"`
	Result *PlaygroundResult `json:"result,omitempty"`
}

const (
	playgroundKeyID     = "admin-playground"
	playgroundMaxTokens = 4096
	playgroundDefault   = 512
)

// preparePlayground builds the synthetic request and enforces Guard scopes.
func (g *Gateway) preparePlayground(ctx context.Context, req PlaygroundRequest) (*PreparedRequest, *model.Route, *PlaygroundResult, error) {
	alias := strings.ToLower(strings.TrimSpace(req.Route))
	if alias == "" {
		return nil, nil, nil, fmt.Errorf("route is required")
	}
	route, err := g.db.RouteByAlias(alias)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("route %q not found", alias)
	}
	if !route.Enabled {
		return nil, route, &PlaygroundResult{
			Route: route.Alias, Status: "error", Error: "route is disabled",
			Trace: []model.TraceStep{},
		}, nil
	}

	messages := make([]provider.ChatMessage, 0, len(req.Messages)+1)
	if s := strings.TrimSpace(req.System); s != "" {
		messages = append(messages, provider.NewTextMessage("system", s))
	}
	messages = append(messages, req.Messages...)
	if len(messages) == 0 {
		return nil, route, nil, fmt.Errorf("at least one message is required")
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = playgroundDefault
	}
	if maxTokens > playgroundMaxTokens {
		maxTokens = playgroundMaxTokens
	}

	prep := &PreparedRequest{
		RequestID: "pg-" + store.RandomHex(8),
		Input: &ChatInput{
			Model: route.Alias, Messages: messages, MaxTokens: maxTokens,
			Temperature: req.Temperature, Stream: req.Stream,
		},
		Route: route,
		// Synthetic key: no policy, no allowed-route restriction, not persisted.
		APIKey:    &model.APIKey{ID: playgroundKeyID, Name: "playground", Prefix: "admin", Enabled: true},
		IP:        "127.0.0.1",
		UserAgent: "myrouter-playground",
		EstTokens: EstimateTokens(messages) + 256,
		Started:   time.Now(),
	}

	primary := g.primaryOf(route)
	if d := g.guard.Check(ctx, prep.GuardRequest(primary)); !d.Allowed {
		return nil, route, &PlaygroundResult{
			Route: route.Alias, RequestID: prep.RequestID, Status: "blocked",
			Provider: provName(primary), ProviderID: idOfProvider(primary),
			Error: fmt.Sprintf("%s (%s scope: %s)", d.Reason, d.Scope, d.PolicyName),
			Trace: []model.TraceStep{{Attempt: 0, Target: route.Alias, Outcome: "guard_blocked",
				Category: d.Scope, StatusCode: d.HTTPStatus, Message: d.Reason}},
		}, nil
	}
	return prep, route, nil, nil
}

// primaryOf resolves the first route target's provider (nil when unset).
func (g *Gateway) primaryOf(route *model.Route) *model.Provider {
	if pt, ok := route.Primary(); ok {
		if p, err := g.db.ProviderByID(pt.ProviderID); err == nil {
			return p
		}
	}
	return nil
}

func (g *Gateway) finishPlayground(prep *PreparedRequest, out *Outcome, res *PlaygroundResult) *PlaygroundResult {
	if out == nil {
		return res
	}
	res.Trace = out.Trace
	res.Provider = provName(out.Provider)
	res.ProviderID = idOfProvider(out.Provider)
	if out.Model != nil {
		res.Model = out.Model.ModelID
		res.ModelID = out.Model.ID
	}
	res.Fallback = out.Fallback
	res.LatencyMS = out.Latency.Milliseconds()
	if out.Response != nil {
		res.InTokens = out.Response.Usage.InputTokens
		res.OutTokens = out.Response.Usage.OutputTokens
	}
	if res.InTokens == 0 && res.OutTokens == 0 {
		res.InTokens, res.OutTokens = out.InTokens, out.OutTokens
	}
	if out.Err != nil {
		res.Status = "error"
		res.Category = string(out.Err.Category)
		res.Error = out.Err.Message
		return res
	}
	res.Status = "ok"
	// Count real tokens against the provider quota so a test cannot be used to
	// silently exceed the upstream account's budget.
	if out.Provider != nil {
		g.guard.AddTokens(prep.GuardRequest(out.Provider), res.InTokens+res.OutTokens)
	}
	return res
}

// Playground runs one non-streaming test call through the routing pipeline.
func (g *Gateway) Playground(ctx context.Context, req PlaygroundRequest) (*PlaygroundResult, error) {
	req.Stream = false
	prep, route, early, err := g.preparePlayground(ctx, req)
	if err != nil {
		return nil, err
	}
	if early != nil {
		return early, nil
	}
	release := g.guard.Acquire(prep.GuardRequest(g.primaryOf(route)))
	defer release()

	out, execErr := g.execute(ctx, prep)
	if execErr != nil {
		return &PlaygroundResult{Route: route.Alias, RequestID: prep.RequestID, Status: "error",
			Error: execErr.Error(), Trace: []model.TraceStep{}}, nil
	}
	res := &PlaygroundResult{Route: route.Alias, RequestID: prep.RequestID}
	if out.Response != nil {
		res.Content = out.Response.Content
	}
	return g.finishPlayground(prep, out, res), nil
}

// PlaygroundStream runs a streaming test call and reports events as they
// arrive, including time-to-first-token.
func (g *Gateway) PlaygroundStream(ctx context.Context, req PlaygroundRequest, emit func(PlaygroundEvent) error) error {
	req.Stream = true
	prep, route, early, err := g.preparePlayground(ctx, req)
	if err != nil {
		return err
	}
	if early != nil {
		if err := emit(PlaygroundEvent{Type: "done", Result: early}); err != nil {
			return err
		}
		return nil
	}
	release := g.guard.Acquire(prep.GuardRequest(g.primaryOf(route)))
	defer release()

	out, execErr := g.execute(ctx, prep)
	if execErr != nil {
		_ = emit(PlaygroundEvent{Type: "error", Error: execErr.Error()})
		return emit(PlaygroundEvent{Type: "done", Result: &PlaygroundResult{
			Route: route.Alias, RequestID: prep.RequestID, Status: "error", Error: execErr.Error(),
		}})
	}
	res := &PlaygroundResult{Route: route.Alias, RequestID: prep.RequestID}
	if err := emit(PlaygroundEvent{Type: "start", Result: &PlaygroundResult{
		Route: route.Alias, RequestID: prep.RequestID,
		Provider: provName(out.Provider), ProviderID: idOfProvider(out.Provider),
	}}); err != nil {
		return err
	}
	if out.Err != nil {
		_ = emit(PlaygroundEvent{Type: "error", Error: out.Err.Message})
		_ = emit(PlaygroundEvent{Type: "done", Result: g.finishPlayground(prep, out, res)})
		return nil
	}
	if out.Stream == nil {
		res.Content = out.Response.Content
		return emit(PlaygroundEvent{Type: "done", Result: g.finishPlayground(prep, out, res)})
	}

	var b strings.Builder
	// TTFT is measured from request preparation (includes dial + upstream
	// first byte), not from the read loop: a fast local upstream finishes the
	// loop-start -> first-delta gap in under a millisecond, which read as 0.
	for {
		select {
		case <-ctx.Done():
			res.Status = "error"
			res.Error = "client disconnected"
			_ = emit(PlaygroundEvent{Type: "done", Result: g.finishPlayground(prep, out, res)})
			return nil
		case ev, ok := <-out.Stream:
			if !ok {
				res.Content = b.String()
				out.Latency = time.Since(prep.Started)
				// Defensive fallback: an upstream that closes without any text
				// (or ignores streaming entirely) should not produce an empty
				// "ok" test result. Retry once without streaming.
				if strings.TrimSpace(res.Content) == "" && res.OutTokens == 0 {
					retryReq := req
					retryReq.Stream = false
					if alt, err := g.Playground(ctx, retryReq); err == nil && alt != nil && alt.Status == "ok" {
						return emit(PlaygroundEvent{Type: "done", Result: alt})
					}
				}
				return emit(PlaygroundEvent{Type: "done", Result: g.finishPlayground(prep, out, res)})
			}
			if ev.Err != nil {
				_ = emit(PlaygroundEvent{Type: "error", Error: ev.Err.Error()})
				out.Err = provider.AsError(ev.Err)
				res.Content = b.String()
				out.Latency = time.Since(prep.Started)
				_ = emit(PlaygroundEvent{Type: "done", Result: g.finishPlayground(prep, out, res)})
				return nil
			}
			if ev.Delta != "" {
				if res.TTFTMS == 0 {
					res.TTFTMS = time.Since(prep.Started).Milliseconds()
				}
				b.WriteString(ev.Delta)
				if err := emit(PlaygroundEvent{Type: "delta", Text: ev.Delta}); err != nil {
					return err
				}
			}
			if ev.InputTok > 0 {
				res.InTokens = ev.InputTok
			}
			if ev.OutputTok > 0 {
				res.OutTokens = ev.OutputTok
			}
			if ev.Finish != "" {
				out.InTokens, out.OutTokens = res.InTokens, res.OutTokens
			}
		}
	}
}
