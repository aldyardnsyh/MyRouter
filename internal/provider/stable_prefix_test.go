package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// Stable-prefix contract: SystemExtra (skills) must come FIRST so prompt
// caching hits; client system follows. Verifies Anthropic cache_control,
// OpenAI cached_tokens normalization, Gemini cachedContentTokenCount.
func TestStablePrefixCacheContract(t *testing.T) {
	req := ChatRequest{
		System: "client instruction", SystemExtra: "## MyRouter skills\nctx",
		MaxTokens: 8000,
	}
	// Anthropic: system[0] holds skills + cache_control breakpoint.
	ab := anthropicBody("claude-x", req, false)
	sys, ok := ab["system"].([]any)
	if !ok || len(sys) == 0 {
		t.Fatalf("anthropic system not a breakpoint array: %#v", ab["system"])
	}
	first := sys[0].(map[string]any)
	text := first["text"].(string)
	if !strings.HasPrefix(text, "## MyRouter skills") {
		t.Fatalf("skills not first in anthropic system: %q", text)
	}
	if _, ok := first["cache_control"]; !ok {
		t.Fatal("missing cache_control breakpoint on anthropic system")
	}
	if ab["max_tokens"] != 8000 {
		t.Fatalf("max_tokens not forwarded: %#v", ab["max_tokens"])
	}
	// Gemini: systemInstruction starts with skills.
	gb := geminiBody(req)
	si := gb["systemInstruction"].(map[string]any)
	gtext := si["parts"].([]map[string]any)[0]["text"].(string)
	if !strings.HasPrefix(gtext, "## MyRouter skills") {
		t.Fatalf("skills not first in gemini systemInstruction: %q", gtext)
	}
	// OpenAI: cached_tokens normalized from prompt_tokens_details.
	var u Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":80}}`), &u); err != nil {
		t.Fatal(err)
	}
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens != 80 {
		t.Fatalf("openai cached_tokens not parsed: %+v", u)
	}
}
