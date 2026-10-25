package logx

import (
	"strings"
	"testing"
)

func TestRedactValueSubstringKeys(t *testing.T) {
	cases := []string{"user_api_key", "refreshToken", "X-Api-Key", "provider_credential", "master_key", "cookie"}
	for _, k := range cases {
		if got := RedactValue(k, "supersecret"); got != "***" {
			t.Errorf("key %q was not redacted: %v", k, got)
		}
	}
	if got := RedactValue("provider", "acme-upstream"); got != "acme-upstream" {
		t.Errorf("benign key was redacted: %v", got)
	}
}

func TestRedactTextPatterns(t *testing.T) {
	secrets := []string{
		// Build credential-shaped strings at runtime: full literals would
		// trip repository secret scanners even though they are synthetic.
		"Authorization: " + "Bearer " + strings.Repeat("a", 12),
		"key is " + "sk-proj-" + strings.Repeat("b", 16),
		"token " + "mr-sk-" + strings.Repeat("c", 12),
		"google " + "AIza" + strings.Repeat("d", 35),
		"groq " + "gsk_" + strings.Repeat("e", 32),
		"xai " + "xai-" + strings.Repeat("f", 24),
		"aws " + "AKIA" + strings.Repeat("Q", 16),
	}
	for _, s := range secrets {
		redacted := RedactText(s)
		if !strings.Contains(redacted, "***") {
			t.Errorf("secret not redacted: %q -> %q", s, redacted)
		}
	}
}

func TestRedactTextKeepsBenignText(t *testing.T) {
	msg := "provider connected in 120ms"
	if got := RedactText(msg); got != msg {
		t.Errorf("benign text modified: %q", got)
	}
}
