package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// NormalizeEffort defaults unknown/empty to medium.
func TestNormalizeEffort(t *testing.T) {
	for in, want := range map[string]string{"low": "low", "medium": "medium", "high": "high", "": "medium", "bogus": "medium"} {
		if got := NormalizeEffort(in); got != want {
			t.Fatalf("NormalizeEffort(%q)=%q want %q", in, got, want)
		}
	}
}

// OpenAI Responses body carries reasoning.effort.
func TestOpenAIResponsesEffort(t *testing.T) {
	a := NewOpenAI(nil)
	body := a.responsesBody("m", ChatRequest{Messages: []ChatMessage{NewTextMessage("user", "hi")}, Effort: "high"}, false)
	r, ok := body["reasoning"].(map[string]any)
	if !ok || r["effort"] != "high" {
		t.Fatalf("responses body missing reasoning.effort=high: %v", body)
	}
}

// Anthropic never receives an unknown field: thinking only from explicit Extra.
func TestAnthropicEffortOptIn(t *testing.T) {
	plain := anthropicBody("m", ChatRequest{Messages: []ChatMessage{NewTextMessage("user", "hi")}, Effort: "high"}, false)
	if _, ok := plain["thinking"]; ok {
		t.Fatal("anthropic body must not include thinking without opt-in")
	}
	opt := anthropicBody("m", ChatRequest{
		Messages: []ChatMessage{NewTextMessage("user", "hi")}, Effort: "high",
		Extra: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024}},
	}, false)
	if _, ok := opt["thinking"]; !ok {
		t.Fatal("anthropic body must forward explicit thinking opt-in")
	}
}

// Gemini never receives an unknown field: thinkingLevel only with opt-in.
func TestGeminiEffortOptIn(t *testing.T) {
	plain := geminiBody(ChatRequest{Messages: []ChatMessage{NewTextMessage("user", "hi")}, Effort: "high"})
	if gen, ok := plain["generationConfig"].(map[string]any); ok {
		if _, has := gen["thinkingLevel"]; has {
			t.Fatal("gemini body must not include thinkingLevel without opt-in")
		}
	}
	opt := geminiBody(ChatRequest{
		Messages: []ChatMessage{NewTextMessage("user", "hi")}, Effort: "low",
		Extra: map[string]any{"thinking": true},
	})
	gen, ok := opt["generationConfig"].(map[string]any)
	if !ok || gen["thinkingLevel"] != "low" {
		t.Fatalf("gemini body missing thinkingLevel=low: %v", opt)
	}
}

// End-to-end: Responses fallback path sends reasoning.effort on the wire.
func TestChatViaResponsesSendsEffort(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"message":"use /v1/responses"}}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"id":"r1","model":"m","output_text":"ok","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()
	a := NewOpenAI(srv.Client())
	resp, err := a.Chat(context.Background(), Credentials{BaseURL: srv.URL}, "m",
		ChatRequest{Messages: []ChatMessage{NewTextMessage("user", "hi")}, Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content=%q", resp.Content)
	}
	r, ok := got["reasoning"].(map[string]any)
	if !ok || r["effort"] != "low" {
		t.Fatalf("wire body missing reasoning.effort=low: %v", got)
	}
	if !strings.Contains(resp.Model, "m") && resp.Model != "" {
		t.Fatalf("model=%q", resp.Model)
	}
}
