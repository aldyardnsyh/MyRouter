package gateway

import (
	"testing"

	"github.com/myrouter/myrouter/internal/provider"
)

// TestBreakerModelScopeKeepsFallbackAlive is the regression test for a route
// whose primary and fallbacks are different models on the same provider: a 5xx
// on the primary used to cool down the whole provider, so every configured
// fallback was skipped and the client got the primary's error.
func TestBreakerModelScopeKeepsFallbackAlive(t *testing.T) {
	b := newBreaker()
	b.fail("prov-1", "model-primary", provider.CatUpstream5xx)

	if ok, _ := b.allow("prov-1", "model-fallback"); !ok {
		t.Fatal("a 5xx on one model must not block another model on the same provider")
	}
	if ok, cat := b.allow("prov-1", "model-primary"); ok || cat != provider.CatUpstream5xx {
		t.Fatalf("failing model should be cooling down, ok=%v cat=%q", ok, cat)
	}

	b.success("prov-1", "model-primary")
	if ok, _ := b.allow("prov-1", "model-primary"); !ok {
		t.Fatal("success must clear the model-scoped cooldown")
	}
}

// TestBreakerRateLimitCoolsWholeProvider keeps account-wide failures
// provider-scoped: a 429 means every model on that credential is exhausted.
func TestBreakerRateLimitCoolsWholeProvider(t *testing.T) {
	b := newBreaker()
	b.fail("prov-1", "model-primary", provider.CatRateLimit)

	if ok, cat := b.allow("prov-1", "model-other"); ok || cat != provider.CatRateLimit {
		t.Fatalf("rate limit must cool the provider, ok=%v cat=%q", ok, cat)
	}
	if ok, _ := b.allow("prov-2", "model-other"); !ok {
		t.Fatal("another provider must stay usable")
	}
}

func TestBreakerBillingAndNetworkAreProviderScoped(t *testing.T) {
	for _, cat := range []provider.ErrorCategory{provider.CatBilling, provider.CatNetwork} {
		b := newBreaker()
		b.fail("prov-1", "model-a", cat)
		if ok, _ := b.allow("prov-1", "model-b"); ok {
			t.Fatalf("%s must cool down the whole provider", cat)
		}
	}
}
