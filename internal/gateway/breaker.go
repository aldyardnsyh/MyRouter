package gateway

import (
	"sync"
	"time"

	"github.com/myrouter/myrouter/internal/provider"
)

// breakerState records how long a provider should be skipped and why.
type breakerState struct {
	until    time.Time
	category provider.ErrorCategory
}

// breaker is a small per-provider cooldown table. It keeps an account that is
// known to be billing-blocked, rate-limited, or unreachable from being retried
// on every request in a multi-account route.
//
// Scope matters: rate limits, billing blocks and network failures are
// account-wide, so they cool down the whole provider. A 5xx or a timeout is
// usually specific to one model deployment, so it cools down that target only.
// Cooling down the provider for every 5xx made a route with several models on
// the same provider skip its entire fallback chain after the primary failed.
type breaker struct {
	mu    sync.Mutex
	state map[string]breakerState
}

func newBreaker() *breaker {
	return &breaker{state: map[string]breakerState{}}
}

// breakerKey returns the cooldown key a failure is recorded under.
func breakerKey(providerID, modelID string, cat provider.ErrorCategory) string {
	switch cat {
	case provider.CatUpstream5xx, provider.CatTimeout:
		if modelID != "" {
			return providerID + "\x00" + modelID
		}
	}
	return providerID
}

// breakerKeys lists every cooldown entry that applies to one target.
func breakerKeys(providerID, modelID string) []string {
	keys := make([]string, 0, 2)
	keys = append(keys, providerID)
	if modelID != "" {
		keys = append(keys, providerID+"\x00"+modelID)
	}
	return keys
}

// allow reports whether a target may be attempted now.
func (b *breaker) allow(providerID, modelID string) (bool, provider.ErrorCategory) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for _, key := range breakerKeys(providerID, modelID) {
		st, ok := b.state[key]
		if !ok {
			continue
		}
		if now.After(st.until) {
			delete(b.state, key)
			continue
		}
		return false, st.category
	}
	return true, ""
}

// fail starts or extends the cooldown for a target. Categories without a
// cooldown are ignored.
func (b *breaker) fail(providerID, modelID string, cat provider.ErrorCategory) {
	d := cooldownFor(cat)
	if d <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state[breakerKey(providerID, modelID, cat)] = breakerState{until: time.Now().Add(d), category: cat}
}

// success clears the cooldown for a target and its provider.
func (b *breaker) success(providerID, modelID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, key := range breakerKeys(providerID, modelID) {
		delete(b.state, key)
	}
}

func cooldownFor(cat provider.ErrorCategory) time.Duration {
	switch cat {
	case provider.CatBilling:
		return 2 * time.Minute
	case provider.CatRateLimit:
		return time.Minute
	case provider.CatTimeout, provider.CatNetwork, provider.CatUpstream5xx:
		return 30 * time.Second
	}
	return 0
}

// nextRR returns the next round-robin start index for a route.
func (g *Gateway) nextRR(routeID string, n int) int {
	if n <= 0 {
		return 0
	}
	g.rrMu.Lock()
	defer g.rrMu.Unlock()
	if g.rr == nil {
		g.rr = map[string]uint64{}
	}
	v := g.rr[routeID]
	g.rr[routeID] = v + 1
	return int(v % uint64(n))
}
