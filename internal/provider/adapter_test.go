package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatMapsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"mock rate limited","type":"rate_limit_exceeded"}}`)
	}))
	defer srv.Close()

	a := NewCompatible(nil)
	_, err := a.Chat(context.Background(), Credentials{BaseURL: srv.URL, APIKey: "k"}, "gpt-test", ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429 upstream")
	}
	pe := AsError(err)
	if pe.Category != CatRateLimit {
		t.Fatalf("category = %s, want rate_limit", pe.Category)
	}
}

func TestChatSuccessRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"1","model":"m","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer srv.Close()

	a := NewCompatible(nil)
	resp, err := a.Chat(context.Background(), Credentials{BaseURL: srv.URL}, "m", ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello" || resp.Usage.TotalTokens != 5 {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestFallbackEligibleEndToEnd(t *testing.T) {
	// Primary 429s; the gateway should surface a fallback-eligible error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	}))
	defer srv.Close()

	a := NewCompatible(nil)
	_, err := a.Chat(context.Background(), Credentials{BaseURL: srv.URL}, "m", ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	pe := AsError(err)
	if !FallbackEligible(pe.Category) {
		t.Fatalf("500 should be fallback eligible, got %s", pe.Category)
	}
}
