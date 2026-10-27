package gateway

import (
	"sort"
	"sync"
	"time"
)

// LiveRequest is one in-flight (or recently finished) gateway call. It powers
// the dashboard routing map so operators can see which provider is consuming
// a key right now.
type LiveRequest struct {
	RequestID      string `json:"request_id"`
	Route          string `json:"route"`
	RequestedModel string `json:"requested_model"`
	ProviderID     string `json:"provider_id"`
	ProviderName   string `json:"provider_name"`
	KeyPrefix      string `json:"key_prefix"`
	IP             string `json:"ip"`
	Status         string `json:"status"`     // active|ok|error|blocked
	StartedAt      int64  `json:"started_at"` // unix ms
	ElapsedMS      int64  `json:"elapsed_ms"`
}

// LiveSnapshot is returned by GET /admin/live.
type LiveSnapshot struct {
	Active []LiveRequest `json:"active"`
	Recent []LiveRequest `json:"recent"`
}

type liveTracker struct {
	mu     sync.Mutex
	active map[string]*LiveRequest
	order  []string
	recent []LiveRequest
}

func newLiveTracker() *liveTracker {
	return &liveTracker{active: map[string]*LiveRequest{}, recent: make([]LiveRequest, 0, 25)}
}

func (t *liveTracker) start(id, route, requested, keyPrefix, ip, providerID, providerName string) {
	if id == "" {
		return
	}
	now := time.Now()
	req := &LiveRequest{
		RequestID: id, Route: route, RequestedModel: requested,
		ProviderID: providerID, ProviderName: providerName,
		KeyPrefix: keyPrefix, IP: ip, Status: "active", StartedAt: now.UnixMilli(),
	}
	t.mu.Lock()
	t.active[id] = req
	t.order = append(t.order, id)
	t.mu.Unlock()
}

func (t *liveTracker) setProvider(id, providerID, providerName string) {
	t.mu.Lock()
	if req, ok := t.active[id]; ok {
		req.ProviderID, req.ProviderName = providerID, providerName
	}
	t.mu.Unlock()
}

// setStatus marks an in-flight request, for example "streaming" once the
// upstream stream has started. It does not move the request out of the active
// set; finish does that.
func (t *liveTracker) setStatus(id, status string) {
	t.mu.Lock()
	if req, ok := t.active[id]; ok {
		req.Status = status
	}
	t.mu.Unlock()
}

// finish moves a request out of the active set. It is idempotent so handlers
// can call it on every path plus a deferred safety net.
func (t *liveTracker) finish(id, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	req, ok := t.active[id]
	if !ok {
		return
	}
	delete(t.active, id)
	for i, v := range t.order {
		if v == id {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	if status == "" {
		status = "ok"
	}
	done := *req
	done.Status = status
	done.ElapsedMS = time.Since(time.UnixMilli(req.StartedAt)).Milliseconds()
	t.recent = append([]LiveRequest{done}, t.recent...)
	if len(t.recent) > 20 {
		t.recent = t.recent[:20]
	}
}

func (t *liveTracker) snapshot() LiveSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	active := make([]LiveRequest, 0, len(t.active))
	for _, id := range t.order {
		req, ok := t.active[id]
		if !ok {
			continue
		}
		cp := *req
		cp.ElapsedMS = time.Since(time.UnixMilli(req.StartedAt)).Milliseconds()
		active = append(active, cp)
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].StartedAt < active[j].StartedAt })
	recent := make([]LiveRequest, len(t.recent))
	copy(recent, t.recent)
	return LiveSnapshot{Active: active, Recent: recent}
}

// LiveSnapshot returns in-flight and recently finished requests for the admin
// dashboard. The slice is a copy: callers never share tracker state.
func (g *Gateway) LiveSnapshot() LiveSnapshot {
	if g.live == nil {
		return LiveSnapshot{Active: []LiveRequest{}, Recent: []LiveRequest{}}
	}
	return g.live.snapshot()
}
