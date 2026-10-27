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
type breaker struct {
	mu    sync.Mutex
	state map[string]breakerState
}

func newBreaker() *breaker {
	return &breaker{state: map[string]breakerState{}}
}

// allow reports whether a provider may be attempted now.
func (b *breaker) allow(providerID string) (bool, provider.ErrorCategory) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.state[providerID]
	if !ok {
		return true, ""
	}
	if time.Now().After(st.until) {
		delete(b.state, providerID)
		return true, ""
	}
	return false, st.category
}

// fail starts or extends the cooldown for a provider. Categories without a
// cooldown are ignored.
func (b *breaker) fail(providerID string, cat provider.ErrorCategory) {
	d := cooldownFor(cat)
	if d <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state[providerID] = breakerState{until: time.Now().Add(d), category: cat}
}

// success clears a provider's cooldown.
func (b *breaker) success(providerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.state, providerID)
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
