package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStreamChatAcceptsJSONBody covers OpenAI-compatible providers that ignore
// `stream: true` and return a normal JSON completion. The adapter must surface
// the content as a single delta instead of an empty stream.
func TestStreamChatAcceptsJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"1","model":"m","choices":[{"message":{"role":"assistant","content":"hello json"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer srv.Close()

	a := NewCompatible(srv.Client())
	ch, err := a.StreamChat(context.Background(), Credentials{BaseURL: srv.URL}, "m", ChatRequest{
		Messages: []ChatMessage{NewTextMessage("user", "hi")},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var text, finish string
	inTok, outTok := 0, 0
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("stream event error: %v", ev.Err)
		}
		text += ev.Delta
		if ev.Finish != "" {
			finish = ev.Finish
		}
		if ev.InputTok > 0 {
			inTok = ev.InputTok
		}
		if ev.OutputTok > 0 {
			outTok = ev.OutputTok
		}
	}
	if text != "hello json" {
		t.Fatalf("expected content from JSON body, got %q", text)
	}
	if finish != "stop" || inTok != 3 || outTok != 2 {
		t.Fatalf("unexpected finish/usage: finish=%q in=%d out=%d", finish, inTok, outTok)
	}
}

// TestStreamChatJSONErrorBody surfaces an error envelope returned with HTTP 200.
func TestStreamChatJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":{"message":"quota exhausted"}}`)
	}))
	defer srv.Close()

	a := NewCompatible(srv.Client())
	_, err := a.StreamChat(context.Background(), Credentials{BaseURL: srv.URL}, "m", ChatRequest{
		Messages: []ChatMessage{NewTextMessage("user", "hi")},
		Stream:   true,
	})
	if err == nil {
		t.Fatal("expected an error for an error envelope")
	}
	if pe := AsError(err); pe == nil || pe.Message != "quota exhausted" {
		t.Fatalf("unexpected error: %v", err)
	}
}
