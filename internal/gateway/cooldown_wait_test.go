package gateway

import (
	"testing"
	"time"

	"github.com/myrouter/myrouter/internal/provider"
)

// TestCooldownWaitHoldsRequest verifies that when every route target is in
// breaker cooldown the request is held (bounded by MYROUTER_COOLDOWN_WAIT)
// instead of failing immediately. The client experiences latency, not a hard
// 429, so a TUI keeps the same turn instead of being cut.
func TestCooldownWaitHoldsRequest(t *testing.T) {
	t.Setenv("MYROUTER_COOLDOWN_WAIT", "250ms")
	g, _, key := newTestGateway(t, "429", "429")

	first := runChat(t, g, key)
	if first.Err == nil || first.Err.Category != provider.CatRateLimit {
		t.Fatalf("first call err = %+v, want rate_limit", first.Err)
	}

	start := time.Now()
	second := runChat(t, g, key)
	elapsed := time.Since(start)

	if second.Err == nil || second.Err.Category != provider.CatRateLimit {
		t.Fatalf("second call err = %+v, want rate_limit", second.Err)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("second call returned after %s; expected it to be held for cooldown", elapsed)
	}
	waited := false
	for _, step := range second.Trace {
		if step.Outcome == "waited" {
			waited = true
		}
	}
	if !waited {
		t.Fatalf("trace lacks waited step: %+v", second.Trace)
	}
}

// TestCooldownWaitDisabledFailsFast verifies MYROUTER_COOLDOWN_WAIT=0 keeps
// the previous fail-fast behavior.
func TestCooldownWaitDisabledFailsFast(t *testing.T) {
	t.Setenv("MYROUTER_COOLDOWN_WAIT", "0")
	g, _, key := newTestGateway(t, "429", "429")
	if first := runChat(t, g, key); first.Err == nil {
		t.Fatal("first call: expected rate_limit")
	}

	start := time.Now()
	second := runChat(t, g, key)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("wait disabled but second call took %s", elapsed)
	}
	if second.Err == nil || second.Err.Category != provider.CatRateLimit {
		t.Fatalf("second call err = %+v, want rate_limit", second.Err)
	}
}
