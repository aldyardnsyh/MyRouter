package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResponsesHintOnlyOnNotFound keeps the fallback narrow: a 404 that names
// /v1/responses is a "wrong endpoint" signal, anything else is a real error.
func TestResponsesHintOnlyOnNotFound(t *testing.T) {
	hint := []byte(`{"error":{"message":"The model \u0060sam-3.1\u0060 is not available on this endpoint. Use /v1/responses."}}`)
	if !responsesHint(http.StatusNotFound, hint) {
		t.Fatal("404 naming /v1/responses must trigger the fallback")
	}
	if responsesHint(http.StatusBadRequest, hint) {
		t.Fatal("400 must not trigger the fallback")
	}
	if responsesHint(http.StatusNotFound, []byte(`{"error":{"message":"no such model"}}`)) {
		t.Fatal("404 without the responses hint must not trigger the fallback")
	}
}

// TestChatFallsBackToResponses proves a chat-completions 404 that points at
// /v1/responses is retried there and normalised back into a ChatResponse.
func TestChatFallsBackToResponses(t *testing.T) {
	var sawResponses bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not available here. Use /v1/responses."}}`))
		case "/v1/responses":
			sawResponses = true
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["input"]; !ok {
				t.Errorf("responses payload missing input: %v", body)
			}
			_, _ = w.Write([]byte(`{"id":"resp_1","model":"sam-3.1","output_text":"pong","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := NewCompatible(srv.Client())
	resp, err := a.Chat(t.Context(), Credentials{BaseURL: srv.URL + "/v1"}, "sam-3.1",
		ChatRequest{Messages: []ChatMessage{NewTextMessage("user", "ping")}})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if !sawResponses {
		t.Fatal("expected a retry against /v1/responses")
	}
	if resp.Content != "pong" {
		t.Fatalf("Content = %q, want %q", resp.Content, "pong")
	}
	if resp.Usage.TotalTokens != 5 {
		t.Fatalf("TotalTokens = %d, want 5", resp.Usage.TotalTokens)
	}
}

// TestResponsesToolCallRoundTrip is the regression guard for the broken tool
// history: a tool result must be sent as function_call_output with its
// call_id, never as a plain role/content message.
func TestResponsesToolCallRoundTrip(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Use /v1/responses."}}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"done"}]}]}`))
	}))
	defer srv.Close()

	toolCalls := json.RawMessage(`[{"id":"call_abc","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]`)
	msgs := []ChatMessage{
		NewTextMessage("system", "be brief"),
		NewTextMessage("user", "read a.txt"),
		{Role: "assistant", ToolCalls: toolCalls},
		{Role: "tool", ToolCallID: "call_abc", Content: json.RawMessage(`"file body"`)},
	}

	a := NewCompatible(srv.Client())
	resp, err := a.Chat(t.Context(), Credentials{BaseURL: srv.URL + "/v1"}, "sam-3.1", ChatRequest{Messages: msgs})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("Content = %q, want %q", resp.Content, "done")
	}

	items, _ := got["input"].([]any)
	if len(items) != 4 {
		t.Fatalf("input items = %d, want 4: %v", len(items), got["input"])
	}
	call, _ := items[2].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_abc" || call["name"] != "read_file" {
		t.Fatalf("assistant tool call not mapped to function_call: %v", call)
	}
	out, _ := items[3].(map[string]any)
	if out["type"] != "function_call_output" || out["call_id"] != "call_abc" {
		t.Fatalf("tool result not mapped to function_call_output: %v", out)
	}
	if !strings.Contains(out["output"].(string), "file body") {
		t.Fatalf("tool output lost: %v", out["output"])
	}
}

// TestStreamFallbackToResponses covers the streaming path of the same fallback.
func TestStreamFallbackToResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Use /v1/responses."}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"he\"}\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"llo\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"))
	}))
	defer srv.Close()

	a := NewCompatible(srv.Client())
	ch, err := a.StreamChat(t.Context(), Credentials{BaseURL: srv.URL + "/v1"}, "sam-3.1",
		ChatRequest{Messages: []ChatMessage{NewTextMessage("user", "hi")}})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}
	var text string
	var outTok int
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		text += ev.Delta
		if ev.OutputTok > 0 {
			outTok = ev.OutputTok
		}
	}
	if text != "hello" {
		t.Fatalf("streamed text = %q, want %q", text, "hello")
	}
	if outTok != 2 {
		t.Fatalf("OutputTok = %d, want 2", outTok)
	}
}
