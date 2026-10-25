package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/myrouter/myrouter/internal/gateway"
	"github.com/myrouter/myrouter/internal/provider"
)

type playgroundPayload struct {
	Route       string                 `json:"route"`
	System      string                 `json:"system"`
	Messages    []provider.ChatMessage `json:"messages"`
	MaxTokens   int                    `json:"max_tokens"`
	Temperature *float64               `json:"temperature"`
}

// decodePlayground validates the test-lab request. The payload is small and
// admin-only, so validation is intentionally strict and cheap.
func decodePlayground(r *http.Request) (gateway.PlaygroundRequest, string) {
	var req playgroundPayload
	if err := decode(r, &req); err != nil {
		return gateway.PlaygroundRequest{}, "invalid JSON body"
	}
	req.Route = strings.TrimSpace(req.Route)
	if req.Route == "" {
		return gateway.PlaygroundRequest{}, "route is required"
	}
	if len(req.Messages) == 0 {
		return gateway.PlaygroundRequest{}, "at least one message is required"
	}
	if len(req.Messages) > 50 {
		return gateway.PlaygroundRequest{}, "too many messages (max 50)"
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user", "assistant", "system":
		default:
			return gateway.PlaygroundRequest{}, "message role must be user, assistant or system"
		}
		if len(m.Content) > 16000 {
			return gateway.PlaygroundRequest{}, "message content is too long (max 16000 chars)"
		}
	}
	if req.MaxTokens < 0 || req.MaxTokens > 4096 {
		return gateway.PlaygroundRequest{}, "max_tokens must be between 1 and 4096"
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return gateway.PlaygroundRequest{}, "temperature must be between 0 and 2"
	}
	return gateway.PlaygroundRequest{
		Route: req.Route, System: strings.TrimSpace(req.System), Messages: req.Messages,
		MaxTokens: req.MaxTokens, Temperature: req.Temperature,
	}, ""
}

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func (s *Server) acquirePlayground() bool {
	select {
	case s.playgroundSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releasePlayground() { <-s.playgroundSem }

// handlePlaygroundChat runs one non-streaming test call and returns timings,
// token usage, provider/model actually used, and the routing trace.
func (s *Server) handlePlaygroundChat(w http.ResponseWriter, r *http.Request, _ *authSession) {
	req, msg := decodePlayground(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", msg)
		return
	}
	if !s.acquirePlayground() {
		writeErr(w, http.StatusTooManyRequests, "playground_busy", "another test is running; try again in a moment")
		return
	}
	defer s.releasePlayground()

	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	res, err := s.gateway.Playground(ctx, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "playground_failed", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": res})
}

// handlePlaygroundStream streams a test call as SSE so the UI can measure
// time-to-first-token and render the answer progressively.
func (s *Server) handlePlaygroundStream(w http.ResponseWriter, r *http.Request, _ *authSession) {
	req, msg := decodePlayground(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", msg)
		return
	}
	if !s.acquirePlayground() {
		writeErr(w, http.StatusTooManyRequests, "playground_busy", "another test is running; try again in a moment")
		return
	}
	defer s.releasePlayground()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "stream_unsupported", "streaming is not supported by this server")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "close")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	emit := func(ev gateway.PlaygroundEvent) error {
		if _, err := w.Write([]byte("data: ")); err != nil {
			return err
		}
		if err := enc.Encode(ev); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()
	if err := s.gateway.PlaygroundStream(ctx, req, emit); err != nil {
		_ = emit(gateway.PlaygroundEvent{Type: "error", Error: err.Error()})
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}
