package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAdaptersMergeSystemExtra verifies the skills injection contract: the
// gateway sets ChatRequest.SystemExtra and every adapter must forward it as
// system context without mutating the client's own messages.
func TestAdaptersMergeSystemExtra(t *testing.T) {
	req := ChatRequest{
		Messages:    []ChatMessage{NewTextMessage("user", "hello")},
		System:      "client system prompt",
		SystemExtra: "## MyRouter skills\nretrieved context",
	}

	t.Run("openai chat completions", func(t *testing.T) {
		a := &OpenAIAdapter{}
		body := a.openAIChatBody("m", req, false)
		msgs, ok := body["messages"].([]ChatMessage)
		if !ok || len(msgs) != 2 {
			t.Fatalf("expected 2 messages, got %#v", body["messages"])
		}
		if msgs[0].Role != "system" || !strings.Contains(msgs[0].Text(), "retrieved context") {
			t.Fatalf("system extra not prepended: %#v", msgs[0])
		}
		if len(req.Messages) != 1 {
			t.Fatal("adapter mutated the request messages")
		}
	})

	t.Run("openai responses api", func(t *testing.T) {
		a := &OpenAIAdapter{}
		body := a.responsesBody("m", req, false)
		input, _ := body["input"].([]map[string]any)
		if len(input) == 0 || input[0]["role"] != "system" || input[0]["content"] != req.SystemExtra {
			t.Fatalf("responses input missing system extra: %#v", body["input"])
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		body := anthropicBody("m", req, false)
		system, _ := body["system"].(string)
		if !strings.Contains(system, "client system prompt") || !strings.Contains(system, "retrieved context") {
			t.Fatalf("anthropic system not merged: %q", system)
		}
	})

	t.Run("gemini", func(t *testing.T) {
		body := geminiBody(req)
		si, _ := body["systemInstruction"].(map[string]any)
		parts, _ := si["parts"].([]map[string]any)
		if len(parts) == 0 {
			t.Fatalf("gemini systemInstruction missing: %#v", body)
		}
		text, _ := parts[0]["text"].(string)
		if !strings.Contains(text, "retrieved context") || !strings.Contains(text, "client system prompt") {
			t.Fatalf("gemini system text not merged: %q", text)
		}
	})
}

func TestImageContentPartsPreservedByOpenAI(t *testing.T) {
	parts, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "describe"},
		{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}},
	})
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: parts}}, SystemExtra: "ctx"}
	body := (&OpenAIAdapter{}).openAIChatBody("m", req, false)
	msgs := body["messages"].([]ChatMessage)
	if len(msgs) != 2 {
		t.Fatalf("expected system + user, got %d", len(msgs))
	}
	if !strings.Contains(string(msgs[1].Content), "image_url") {
		t.Fatal("image part was lost")
	}
}
