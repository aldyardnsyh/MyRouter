// Package guard implements access control and usage limiting (PRD §7):
// token-bucket RPM/TPM, daily/monthly quotas, concurrency, unique IP/device
// caps, CIDR IP rules, cooldowns and auto-blocking. Counters live in memory
// (sharded maps) and are snapshotted to SQLite so limits survive restarts.
package guard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// Scope identifies one dimension of the evaluation hierarchy.
type Scope struct {
	Type  string // global|provider|route|api_key|ip|device
	ID    string
	Label string
}

// Decision is the outcome of a Guard check.
type Decision struct {
	Allowed    bool   `json:"allowed"`
	HTTPStatus int    `json:"http_status"`
	Scope      string `json:"scope"`
	PolicyID   string `json:"policy_id"`
	PolicyName string `json:"policy_name"`
	Reason     string `json:"reason"`
	RetryAfter int    `json:"retry_after"`
	// OnExceed is the configured behavior: reject|queue|fallback.
	OnExceed string `json:"on_exceed"`
	// QueueMaxS is the maximum time a caller may be queued when OnExceed is
	// "queue". Zero means queueing is disabled and the request is rejected.
	QueueMaxS int `json:"queue_max_s"`
	// FallbackTo optionally names a route to redirect to.
	FallbackTo string `json:"fallback_to"`
}

// Request describes the inbound call being guarded.
type Request struct {
	APIKey    *model.APIKey
	Route     *model.Route
	Provider  *model.Provider
	IP        string
	DeviceID  string
	DeviceFP  string
	UserAgent string
	Model     string
	EstTokens int
}

// Engine evaluates Guard rules.
type Engine struct {
	db     *store.DB
	log    Logger
	mu     sync.RWMutex
	bucket map[string]*bucket // key: policyID|scopeType|scopeID|metric
	ips    map[string]*ipWindow
	cons   map[string]int

	stop chan struct{}
	once sync.Once
}

// Logger is the subset of logx used by Guard.
type Logger interface {
	Warn(msg string, fields ...Field)
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
}

// Field mirrors logx.Field to avoid an import cycle in tests.
type Field struct {
	Key   string
	Value any
}

// New constructs a Guard engine and restores persisted counters.
func New(db *store.DB, log Logger) *Engine {
	e := &Engine{
		db:     db,
		log:    log,
		bucket: map[string]*bucket{},
		ips:    map[string]*ipWindow{},
		cons:   map[string]int{},
		stop:   make(chan struct{}),
	}
	e.restore()
	return e
}

// bucket is a token bucket with a sliding window for rate metrics.
type bucket struct {
	tokens   float64
	last     time.Time
	consumed float64
	limit    float64
}

func (b *bucket) refill(now time.Time, perSecond float64, burst float64) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * perSecond
		if b.tokens > burst {
			b.tokens = burst
		}
		b.last = now
	}
}

type ipWindow struct {
	seen  map[string]int64 // ip -> first seen
	start time.Time
}

// identityWindow is 24h for max_ips_per_key.
const identityWindow = 24 * time.Hour

func metricKey(policyID, scopeType, scopeID, metric string) string {
	return policyID + "|" + scopeType + "|" + scopeID + "|" + metric
}

// ---------------------------------------------------------------------------
// Evaluation
// ---------------------------------------------------------------------------

// Check evaluates the full hierarchy. All limits are checked; the most
// specific failing scope wins for the error message.
func (e *Engine) Check(ctx context.Context, req *Request) Decision {
	now := time.Now()
	scopes := e.scopesFor(req)

	var policies []*model.Policy
	var policyScopes []Scope
	for _, s := range scopes {
		p, err := e.db.PolicyForScope(s.Type, s.ID)
		if err == nil && p != nil {
			policies = append(policies, p)
			policyScopes = append(policyScopes, s)
		}
	}

	// Deny-by-default block list check (403).
	if d, blocked := e.checkBlocks(req, now); blocked {
		return d
	}

	// IP rule evaluation happens before limits (403).
	if d, denied := e.checkIPRules(req); denied {
		e.recordViolation(req, d, now)
		return d
	}

	// Most specific policy is last; evaluate all, keep the most specific fail.
	var failure *Decision
	for i, p := range policies {
		scope := policyScopes[i]
		if d, ok := e.checkPolicy(ctx, req, p, scope, now); !ok {
			d := d
			failure = &d
		}
	}
	if failure != nil {
		e.recordViolation(req, *failure, now)
		return *failure
	}

	// Whitelist checks that do not depend on a policy.
	if req.APIKey != nil && len(store.SplitCSV(req.APIKey.AllowedRoutes)) > 0 && req.Route != nil {
		allowed := store.SplitCSV(req.APIKey.AllowedRoutes)
		if !containsFold(allowed, req.Route.Alias) {
			d := Decision{
				Allowed: false, HTTPStatus: http.StatusForbidden, Scope: "api_key",
				Reason: fmt.Sprintf("route %q is not allowed for this key", req.Route.Alias),
			}
			return d
		}
	}

	return Decision{Allowed: true}
}

func (e *Engine) scopesFor(req *Request) []Scope {
	scopes := []Scope{{Type: "global", ID: "*", Label: "Global"}}
	if req.Provider != nil {
		scopes = append(scopes, Scope{Type: "provider", ID: req.Provider.ID, Label: req.Provider.Name})
	}
	if req.Route != nil {
		scopes = append(scopes, Scope{Type: "route", ID: req.Route.ID, Label: req.Route.Alias})
	}
	if req.APIKey != nil {
		scopes = append(scopes, Scope{Type: "api_key", ID: req.APIKey.ID, Label: req.APIKey.Name})
	}
	if req.IP != "" {
		scopes = append(scopes, Scope{Type: "ip", ID: req.IP, Label: req.IP})
	}
	if req.DeviceID != "" {
		scopes = append(scopes, Scope{Type: "device", ID: req.DeviceID, Label: req.DeviceID})
	}
	return scopes
}

// checkPolicy evaluates every limit of a single policy.
func (e *Engine) checkPolicy(ctx context.Context, req *Request, p *model.Policy, scope Scope, now time.Time) (Decision, bool) {
	key := func(metric string) string {
		id := scope.ID
		if req.APIKey != nil {
			id += ":" + req.APIKey.ID
		}
		return metricKey(p.ID, scope.Type, id, metric)
	}

	if p.MaxTokensReq > 0 && req.EstTokens > p.MaxTokensReq {
		return e.exceeded(p, scope, req, fmt.Sprintf("request exceeds max_tokens_per_request (%d > %d)", req.EstTokens, p.MaxTokensReq), 0), false
	}

	// Unique IPs per key over 24h.
	if p.MaxIPs > 0 && req.APIKey != nil && req.IP != "" {
		if !e.withinIPLimit(p.ID, req.APIKey.ID, req.IP, p.MaxIPs, now) {
			return e.exceeded(p, scope, req, fmt.Sprintf("key used from more than %d unique IPs in 24h", p.MaxIPs), 0), false
		}
	}

	// Unique devices per key.
	if p.MaxDevices > 0 && req.APIKey != nil && req.DeviceFP != "" {
		n, err := e.db.CountDevicesForKey(req.APIKey.ID)
		if err == nil && n > p.MaxDevices {
			return e.exceeded(p, scope, req, fmt.Sprintf("key used from more than %d unique devices", p.MaxDevices), 0), false
		}
	}

	// RPM token bucket.
	if p.RPM > 0 {
		ok, retry := e.consume(key("rpm"), float64(p.RPM)/60.0, float64(e.burst(p)), now)
		if !ok {
			return e.exceeded(p, scope, req, fmt.Sprintf("rate limit exceeded: %d requests/min on %s", p.RPM, scope.Label), retry), false
		}
	}

	// TPM token bucket (estimated up front, corrected after the response).
	if p.TPM > 0 && req.EstTokens > 0 {
		ok, retry := e.consumeN(key("tpm"), float64(p.TPM)/60.0, float64(p.TPM), float64(req.EstTokens), now)
		if !ok {
			return e.exceeded(p, scope, req, fmt.Sprintf("token limit exceeded: %d tokens/min on %s", p.TPM, scope.Label), retry), false
		}
	}

	// Concurrent requests. The gateway pairs this with Acquire/Release; when the
	// caller does not manage concurrency (e.g. a dry-run check) the slot is
	// released immediately.
	if p.MaxConcurrent > 0 {
		e.mu.Lock()
		ck := metricKey(p.ID, scope.Type, scope.ID, "conc")
		if e.cons[ck] >= p.MaxConcurrent {
			e.mu.Unlock()
			return e.exceeded(p, scope, req, fmt.Sprintf("concurrency limit reached: %d in flight on %s", p.MaxConcurrent, scope.Label), 1), false
		}
		e.cons[ck]++
		e.mu.Unlock()
		if ctx == nil || ctx.Value(contextMarker{}) == nil {
			e.mu.Lock()
			e.cons[ck]--
			e.mu.Unlock()
		}
	}

	// Daily / monthly quotas.
	if p.DailyTokens > 0 {
		used := e.windowUsed(key("daily"), 24*time.Hour, now)
		if used+float64(req.EstTokens) > float64(p.DailyTokens) {
			return e.exceeded(p, scope, req, fmt.Sprintf("daily token quota exhausted (%d/day on %s)", p.DailyTokens, scope.Label), int(secondsUntilReset(now))), false
		}
	}
	if p.MonthlyTokens > 0 {
		used := e.windowUsed(key("monthly"), 30*24*time.Hour, now)
		if used+float64(req.EstTokens) > float64(p.MonthlyTokens) {
			return e.exceeded(p, scope, req, fmt.Sprintf("monthly token quota exhausted (%d/month on %s)", p.MonthlyTokens, scope.Label), int(secondsUntilReset(now))), false
		}
	}

	return Decision{}, true
}

func (e *Engine) burst(p *model.Policy) int {
	if p.Burst > 0 {
		return p.Burst
	}
	return p.RPM
}

func (e *Engine) exceeded(p *model.Policy, scope Scope, req *Request, reason string, retryAfter int) Decision {
	if retryAfter <= 0 {
		retryAfter = 1
	}
	return Decision{
		Allowed:    false,
		HTTPStatus: http.StatusTooManyRequests,
		Scope:      scope.Type,
		PolicyID:   p.ID,
		PolicyName: p.Name,
		Reason:     reason,
		RetryAfter: retryAfter,
		OnExceed:   p.OnExceed,
		QueueMaxS:  p.QueueMaxS,
	}
}

func (e *Engine) consume(key string, ratePerSec, burst float64, now time.Time) (bool, int) {
	return e.consumeN(key, ratePerSec, burst, 1, now)
}

func (e *Engine) consumeN(key string, ratePerSec, burst, cost float64, now time.Time) (bool, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.bucket[key]
	if !ok || now.Sub(b.last) > 10*time.Minute {
		b = &bucket{tokens: burst, last: now}
		e.bucket[key] = b
	}
	b.refill(now, ratePerSec, burst)
	if b.tokens < cost {
		retry := 1
		if ratePerSec > 0 {
			retry = int((cost - b.tokens) / ratePerSec)
			if retry < 1 {
				retry = 1
			}
		}
		return false, retry
	}
	b.tokens -= cost
	return true, 0
}

func (e *Engine) windowUsed(key string, window time.Duration, now time.Time) float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	b, ok := e.bucket[key]
	if !ok || now.Sub(b.last) > window {
		return 0
	}
	return b.consumedLimit() - b.tokens
}

// consumedLimit returns the consumed amount for fixed windows.
func (b *bucket) consumedLimit() float64 { return b.consumed }

// AddTokens credits actual usage after a response so TPM/quotas reflect
// real token counts rather than the estimate.
func (e *Engine) AddTokens(req *Request, actual int) {
	if req == nil || actual <= 0 {
		return
	}
	now := time.Now()
	scopes := e.scopesFor(req)
	policies := make([]*model.Policy, 0, len(scopes))
	for _, s := range scopes {
		if p, err := e.db.PolicyForScope(s.Type, s.ID); err == nil && p != nil {
			policies = append(policies, p)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, p := range policies {
		scope := scopes[i]
		id := scope.ID
		if req.APIKey != nil {
			id += ":" + req.APIKey.ID
		}
		delta := float64(actual - req.EstTokens)
		if p.TPM > 0 && delta != 0 {
			k := metricKey(p.ID, scope.Type, id, "tpm")
			if b, ok := e.bucket[k]; ok {
				b.tokens -= delta
			}
		}
		if p.DailyTokens > 0 {
			e.addWindow(metricKey(p.ID, scope.Type, id, "daily"), float64(actual), 24*time.Hour, now)
		}
		if p.MonthlyTokens > 0 {
			e.addWindow(metricKey(p.ID, scope.Type, id, "monthly"), float64(actual), 30*24*time.Hour, now)
		}
	}
}

func (e *Engine) addWindow(key string, amount float64, window time.Duration, now time.Time) {
	b, ok := e.bucket[key]
	if !ok || now.Sub(b.last) > window {
		b = &bucket{tokens: 0, last: now}
		e.bucket[key] = b
	}
	b.consumed += amount
}

func (e *Engine) withinIPLimit(policyID, apiKeyID, ip string, max int, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	k := policyID + "|" + apiKeyID + "|ips"
	w, ok := e.ips[k]
	if !ok || now.Sub(w.start) > identityWindow {
		w = &ipWindow{seen: map[string]int64{}, start: now}
		e.ips[k] = w
	}
	if _, seen := w.seen[ip]; seen {
		return true
	}
	if len(w.seen) >= max {
		return false
	}
	w.seen[ip] = now.Unix()
	return true
}

// Acquire increments concurrency counters for a request and returns a release
// function. Called by the gateway around provider execution.
func (e *Engine) Acquire(req *Request) func() {
	scopes := e.scopesFor(req)
	keys := make([]string, 0, len(scopes))
	for _, s := range scopes {
		p, err := e.db.PolicyForScope(s.Type, s.ID)
		if err != nil || p == nil || p.MaxConcurrent <= 0 {
			continue
		}
		keys = append(keys, metricKey(p.ID, s.Type, s.ID, "conc"))
	}
	if len(keys) == 0 {
		return func() {}
	}
	e.mu.Lock()
	for _, k := range keys {
		e.cons[k]++
	}
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		for _, k := range keys {
			if e.cons[k] > 0 {
				e.cons[k]--
			}
		}
		e.mu.Unlock()
	}
}

// contextMarker signals that the caller manages concurrency via Acquire.
type contextMarker struct{}

// ---------------------------------------------------------------------------
// IP rules & blocks
// ---------------------------------------------------------------------------

func (e *Engine) checkIPRules(req *Request) (Decision, bool) {
	rules, err := e.db.ListIPRules()
	if err != nil || len(rules) == 0 || req.IP == "" {
		return Decision{}, false
	}
	ip := net.ParseIP(req.IP)
	if ip == nil {
		return Decision{}, false
	}
	now := store.Now()
	var allowedByRule bool
	for _, r := range rules {
		if r.ExpiresAt != nil && *r.ExpiresAt <= now {
			continue
		}
		if r.ScopeType == "api_key" && (req.APIKey == nil || r.ScopeID != req.APIKey.ID) {
			continue
		}
		_, ipnet, err := net.ParseCIDR(normalizeCIDR(r.CIDR))
		if err != nil {
			continue
		}
		if !ipnet.Contains(ip) {
			continue
		}
		if r.Action == "deny" {
			return Decision{
				Allowed: false, HTTPStatus: http.StatusForbidden, Scope: "ip",
				Reason: fmt.Sprintf("ip %s is denied by rule %s", req.IP, r.CIDR),
			}, true
		}
		allowedByRule = true
	}
	// If a key has allow rules and the IP is not in them, reject.
	hasAllow := false
	for _, r := range rules {
		if r.Action == "allow" && r.ScopeType == "api_key" && req.APIKey != nil && r.ScopeID == req.APIKey.ID {
			hasAllow = true
		}
	}
	if hasAllow && !allowedByRule {
		return Decision{
			Allowed: false, HTTPStatus: http.StatusForbidden, Scope: "ip",
			Reason: fmt.Sprintf("ip %s is not in the allowlist for this key", req.IP),
		}, true
	}
	return Decision{}, false
}

func (e *Engine) checkBlocks(req *Request, now time.Time) (Decision, bool) {
	blocks, err := e.db.ListBlocks()
	if err != nil {
		return Decision{}, false
	}
	nowUnix := now.Unix()
	for _, b := range blocks {
		if b.Until != nil && *b.Until <= nowUnix {
			continue
		}
		matched := false
		switch b.Kind {
		case "ip":
			matched = b.Value == req.IP
		case "device":
			matched = b.Value == req.DeviceID || b.Value == req.DeviceFP
		case "key":
			matched = req.APIKey != nil && (b.Value == req.APIKey.ID || b.Value == req.APIKey.Prefix)
		}
		if matched {
			return Decision{
				Allowed: false, HTTPStatus: http.StatusForbidden, Scope: b.Kind,
				Reason: "blocked: " + b.Reason,
			}, true
		}
	}
	return Decision{}, false
}

// recordViolation implements auto-block: X violations in M minutes triggers a
// temporary block of the offending IP.
func (e *Engine) recordViolation(req *Request, d Decision, now time.Time) {
	scopes := e.scopesFor(req)
	for _, s := range scopes {
		p, err := e.db.PolicyForScope(s.Type, s.ID)
		if err != nil || p == nil || p.AutoBlockN <= 0 || p.AutoBlockM <= 0 {
			continue
		}
		key := metricKey(p.ID, "violations", s.Type+":"+s.ID, "count")
		e.mu.Lock()
		b, ok := e.bucket[key]
		window := time.Duration(p.AutoBlockM) * time.Minute
		if !ok || now.Sub(b.last) > window {
			b = &bucket{tokens: 0, last: now}
			e.bucket[key] = b
		}
		b.consumed += 1
		hits := b.consumed
		e.mu.Unlock()
		if int(hits) >= p.AutoBlockN {
			duration := p.AutoBlockS
			if duration <= 0 {
				duration = 300
			}
			until := now.Add(time.Duration(duration) * time.Second).Unix()
			target := req.IP
			kind := "ip"
			if target == "" && req.APIKey != nil {
				target = req.APIKey.ID
				kind = "key"
			}
			if target == "" {
				continue
			}
			// Avoid duplicate blocks for the same value.
			existing, _ := e.db.ListBlocks()
			dup := false
			for _, ex := range existing {
				if ex.Kind == kind && ex.Value == target && ex.Until != nil && *ex.Until > now.Unix() {
					dup = true
				}
			}
			if dup {
				continue
			}
			_ = e.db.CreateBlock(&model.BlockEntry{
				Kind: kind, Value: target, Reason: fmt.Sprintf("auto-block after %d violations in %d min (policy %s)", p.AutoBlockN, p.AutoBlockM, p.Name),
				Until: &until,
			})
			if e.log != nil {
				e.log.Warn("guard auto-block", Field{"kind", kind}, Field{"value", target}, Field{"until", until})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// Snapshot writes live counters to SQLite. Called periodically.
func (e *Engine) Snapshot() {
	now := time.Now()
	e.mu.RLock()
	type row struct {
		key    string
		tokens float64
		count  float64
	}
	rows := make([]row, 0, len(e.bucket))
	for k, b := range e.bucket {
		rows = append(rows, row{k, b.tokens, b.consumed})
	}
	e.mu.RUnlock()
	for _, r := range rows {
		_ = e.db.SnapshotCounter(r.key, now.Unix(), r.tokens, r.count)
	}
}

func (e *Engine) restore() {
	since := time.Now().Add(-31 * 24 * time.Hour).Unix()
	vals, windows, err := e.db.LoadCounters(since)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range vals {
		b := &bucket{tokens: v[0], consumed: v[1], last: time.Unix(windows[k], 0)}
		e.bucket[k] = b
	}
}

// Start launches the periodic snapshot/cleanup loop.
func (e *Engine) Start(ctx context.Context) {
	e.once.Do(func() {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			cleanup := time.NewTicker(5 * time.Minute)
			defer cleanup.Stop()
			for {
				select {
				case <-ctx.Done():
					e.Snapshot()
					return
				case <-ticker.C:
					e.Snapshot()
				case <-cleanup.C:
					e.cleanup()
				}
			}
		}()
	})
}

func (e *Engine) cleanup() {
	now := time.Now()
	e.mu.Lock()
	for k, b := range e.bucket {
		if now.Sub(b.last) > 31*24*time.Hour {
			delete(e.bucket, k)
		}
	}
	for k, w := range e.ips {
		if now.Sub(w.start) > identityWindow {
			delete(e.ips, k)
		}
	}
	e.mu.Unlock()
	_ = e.db.PurgeCounters(now.Add(-31 * 24 * time.Hour).Unix())
	_ = e.db.PurgeExpiredBlocks(now.Unix())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func secondsUntilReset(now time.Time) int64 {
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	return int64(midnight.Sub(now).Seconds())
}

func normalizeCIDR(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		if strings.Contains(s, ":") {
			return s + "/128"
		}
		return s + "/32"
	}
	return s
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}
