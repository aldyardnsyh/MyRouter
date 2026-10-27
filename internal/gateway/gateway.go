// Package gateway implements the request pipeline (PRD §8):
// auth key -> guard -> route resolve -> provider adapter -> normalize -> log.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

// Gateway executes OpenAI-compatible gateway requests.
type Gateway struct {
	db       *store.DB
	registry *provider.Registry
	box      *secret.Box
	guard    *guard.Engine
	identity *guard.Identity
	client   *http.Client
	log      *logx.Logger
	version  string
	live     *liveTracker
	breaker  *breaker

	identityMu sync.RWMutex
	rrMu       sync.Mutex
	rr         map[string]uint64
}

// Options configures a Gateway.
type Options struct {
	DB       *store.DB
	Registry *provider.Registry
	Box      *secret.Box
	Guard    *guard.Engine
	Identity *guard.Identity
	Client   *http.Client
	Log      *logx.Logger
	Version  string
}

// New builds a Gateway.
func New(o Options) *Gateway {
	if o.Client == nil {
		o.Client = provider.NewHTTPClient()
	}
	if o.Log == nil {
		o.Log = logx.Default()
	}
	return &Gateway{
		db: o.DB, registry: o.Registry, box: o.Box, guard: o.Guard,
		identity: o.Identity, client: o.Client, log: o.Log, version: o.Version,
		live: newLiveTracker(), breaker: newBreaker(), rr: map[string]uint64{},
	}
}

// SetIdentity swaps the trusted-proxy configuration (called when settings change).
func (g *Gateway) SetIdentity(id *guard.Identity) {
	g.identityMu.Lock()
	g.identity = id
	g.identityMu.Unlock()
}

func (g *Gateway) identityFor() *guard.Identity {
	g.identityMu.RLock()
	defer g.identityMu.RUnlock()
	return g.identity
}

// OpenAIModel is the /v1/models entry shape.
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ListModels returns enabled route aliases as OpenAI-style models.
func (g *Gateway) ListModels(ctx context.Context) ([]OpenAIModel, error) {
	routes, err := g.db.ListRoutes()
	if err != nil {
		return nil, err
	}
	out := make([]OpenAIModel, 0, len(routes))
	for _, r := range routes {
		if !r.Enabled || !routeHasUsableTarget(r) {
			continue
		}
		out = append(out, OpenAIModel{ID: r.Alias, Object: "model", Created: r.CreatedAt, OwnedBy: "myrouter"})
	}
	return out, nil
}

// routeHasUsableTarget reports whether any target can serve. The store folds
// provider.enabled and model.available into Target.Available, so a route that
// only points at disabled providers is not advertised to clients.
func routeHasUsableTarget(r model.Route) bool {
	for _, t := range r.Targets {
		if t.Available {
			return true
		}
	}
	return false
}

// AuthResult is the outcome of API-key authentication.
type AuthResult struct {
	Key *model.APIKey
	IP  string
}

// Authenticate resolves a bearer token to an API key.
func (g *Gateway) Authenticate(r *http.Request) (*AuthResult, error) {
	token := bearerToken(r)
	if token == "" {
		return nil, errUnauthorized("missing API key")
	}
	key, err := g.db.APIKeyByHash(store.HashToken(token))
	if err != nil {
		return nil, errUnauthorized("invalid API key")
	}
	if !key.Enabled {
		return nil, errUnauthorized("API key is disabled")
	}
	if key.ExpiresAt != nil && *key.ExpiresAt <= store.Now() {
		return nil, errUnauthorized("API key has expired")
	}
	return &AuthResult{Key: key, IP: g.identityFor().ClientIP(r)}, nil
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	// OpenAI clients sometimes use api-key header (Azure style).
	if v := r.Header.Get("api-key"); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// ChatInput is a parsed OpenAI chat request.
type ChatInput struct {
	Model       string
	Messages    []provider.ChatMessage
	System      string
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	Stream      bool
	Headers     http.Header
	Extra       map[string]any
	// SystemExtra is server-side context assembled by skills (retrieval, notes,
	// vision). Adapters merge it after the client's own system prompt.
	SystemExtra string
}

// ParseChat decodes an OpenAI chat completions body.
func ParseChat(body []byte) (*ChatInput, error) {
	var raw struct {
		Model       string                 `json:"model"`
		Messages    []provider.ChatMessage `json:"messages"`
		MaxTokens   int                    `json:"max_tokens"`
		Temperature *float64               `json:"temperature"`
		TopP        *float64               `json:"top_p"`
		Stream      bool                   `json:"stream"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON body")
	}
	if strings.TrimSpace(raw.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if len(raw.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}
	in := &ChatInput{
		Model: raw.Model, Messages: raw.Messages, MaxTokens: raw.MaxTokens,
		Temperature: raw.Temperature, TopP: raw.TopP, Stream: raw.Stream,
		Extra: map[string]any{},
	}
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err == nil {
		for _, k := range []string{"stop", "presence_penalty", "frequency_penalty", "seed", "response_format", "user", "top_k", "tools", "tool_choice", "parallel_tool_calls", "reasoning_effort", "reasoning"} {
			if v, ok := generic[k]; ok {
				in.Extra[k] = v
			}
		}
	}
	for _, m := range in.Messages {
		if m.Role == "system" {
			if in.System != "" {
				in.System += "\n\n"
			}
			in.System += m.Text()
		}
	}
	return in, nil
}

// EstimateTokens approximates input tokens (4 chars ≈ 1 token).
func EstimateTokens(messages []provider.ChatMessage) int {
	n := 0
	for _, m := range messages {
		n += len(m.Content)/4 + 4
	}
	if n < 1 {
		n = 1
	}
	return n
}

// PreparedRequest bundles everything needed to execute a call.
type PreparedRequest struct {
	RequestID string
	Input     *ChatInput
	Route     *model.Route
	APIKey    *model.APIKey
	IP        string
	DeviceID  string
	DeviceFP  string
	UserAgent string
	EstTokens int
	Started   time.Time

	// Skills state for this request.
	SkillTrace    string
	CaptureSkills []model.Skill
	CaptureUser   string
}

// GuardRequest builds the Guard evaluation request.
func (p *PreparedRequest) GuardRequest(prov *model.Provider) *guard.Request {
	return &guard.Request{
		APIKey: p.APIKey, Route: p.Route, Provider: prov, IP: p.IP,
		DeviceID: p.DeviceID, DeviceFP: p.DeviceFP, UserAgent: p.UserAgent,
		Model: p.Input.Model, EstTokens: p.EstTokens,
	}
}

// Outcome is the result of executing a request.
type Outcome struct {
	Response *provider.ChatResponse
	Stream   <-chan provider.StreamEvent
	Model    *model.Model
	Provider *model.Provider
	Trace    []model.TraceStep
	Err      *provider.Error
	Fallback bool
	Latency  time.Duration
	// StreamStarted reports whether the upstream stream began successfully.
	StreamStarted bool
	InTokens      int
	OutTokens     int
}

// AttemptResult is the normalized result of one target attempt.
type AttemptResult struct {
	Resp    *provider.ChatResponse
	Stream  <-chan provider.StreamEvent
	Err     *provider.Error
	Latency time.Duration
}

// decryptCredential loads and decrypts a provider credential.
func (g *Gateway) decryptCredential(providerID string) (string, error) {
	ct, nonce, err := g.db.CredentialFor(providerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return g.box.Decrypt(ct, nonce)
}

// maxTargetAttempts bounds how many real upstream calls one request may make
// when a route contains a large account pool. Cheap skips (disabled provider,
// cooldown) do not count; only calls that reach a provider do.
const maxTargetAttempts = 12

// execute attempts the primary target then each fallback in order. Fallback is
// only triggered for categories listed in the route's fallback_on set
// (default: 429, timeout, 5xx, network).
func (g *Gateway) execute(ctx context.Context, prep *PreparedRequest) (*Outcome, error) {
	route, err := g.db.RouteByID(prep.Route.ID)
	if err != nil {
		return nil, err
	}
	if !route.Enabled {
		return nil, &AuthError{Message: "route is disabled"}
	}
	if len(route.Targets) == 0 {
		return nil, &AuthError{Message: "route has no targets"}
	}

	eligible := fallbackSet(route.FallbackOn)
	retries := route.Retry
	if retries < 0 {
		retries = 0
	}
	timeout := time.Duration(route.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	budget := len(route.Targets)
	if budget > maxTargetAttempts {
		budget = maxTargetAttempts
	}
	totalDeadline := prep.Started.Add(timeout + time.Duration(budget)*timeout/2)

	// round_robin rotates the starting account per request so a pool of
	// accounts shares load. priority keeps the declared order and only moves
	// to the next target when the current one fails.
	targets := route.Targets
	if route.Strategy == "round_robin" && len(targets) > 1 {
		start := g.nextRR(route.ID, len(targets))
		ordered := make([]model.RouteTarget, 0, len(targets))
		ordered = append(ordered, targets[start:]...)
		ordered = append(ordered, targets[:start]...)
		targets = ordered
	}

	out := &Outcome{Trace: []model.TraceStep{}, Provider: &model.Provider{}}
	var lastCooldown *provider.Error
	attempt := 0
	networkAttempts := 0
	for idx, t := range targets {
		if networkAttempts >= maxTargetAttempts {
			break
		}
		mdl, err := g.db.ModelByID(t.ModelID)
		if err != nil {
			continue
		}
		prov, err := g.db.ProviderByID(mdl.ProviderID)
		if err != nil || !prov.Enabled || !mdl.Available {
			out.Trace = append(out.Trace, model.TraceStep{
				Attempt: attempt + 1, Target: mdl.ModelID, Provider: provName(prov), Outcome: "skipped",
				Category: "unavailable", Message: "provider disabled or model unavailable",
			})
			attempt++
			continue
		}
		if allowed, cooldownCat := g.breaker.allow(prov.ID, mdl.ModelID); !allowed {
			lastCooldown = &provider.Error{
				Category: cooldownCat,
				Message:  "provider cooling down after " + string(cooldownCat),
			}
			out.Trace = append(out.Trace, model.TraceStep{
				Attempt: attempt + 1, Target: mdl.ModelID, Provider: prov.Name, Outcome: "skipped",
				Category: "cooldown", Message: lastCooldown.Message,
			})
			attempt++
			continue
		}

		var lastErr *provider.Error
		for r := 0; r <= retries; r++ {
			if networkAttempts >= maxTargetAttempts {
				break
			}
			networkAttempts++
			attempt++
			attemptCtx, cancel := context.WithDeadline(ctx, minTime(totalDeadline, time.Now().Add(timeout)))
			start := time.Now()
			res, perr := g.attemptOnce(attemptCtx, prep, mdl, prov)
			latency := time.Since(start)

			if perr == nil {
				out.Trace = append(out.Trace, model.TraceStep{
					Attempt: attempt, Target: mdl.ModelID, Provider: prov.Name, Outcome: "success",
					StatusCode: res.StatusCode, LatencyMS: latency.Milliseconds(),
				})
				out.Model = mdl
				out.Provider = prov
				out.Response = res.Resp
				// Streaming responses return while the upstream body is still
				// being read. The per-attempt context must stay alive until the
				// stream is drained, otherwise cancel() would abort it before
				// the first chunk reaches the client.
				if res.Stream != nil {
					out.Stream = cancelWhenDone(ctx, res.Stream, cancel)
				} else {
					out.Stream = nil
					cancel()
				}
				out.StreamStarted = res.Stream != nil
				out.Fallback = idx > 0
				out.Latency = latency
				out.Err = nil // a later target succeeded; clear earlier failures
				g.breaker.success(prov.ID, mdl.ModelID)
				return out, nil
			}
			cancel()

			lastErr = perr
			out.Trace = append(out.Trace, model.TraceStep{
				Attempt: attempt, Target: mdl.ModelID, Provider: prov.Name, Outcome: "error",
				Category: string(perr.Category), StatusCode: perr.StatusCode,
				LatencyMS: latency.Milliseconds(), Message: perr.Message,
			})
			g.breaker.fail(prov.ID, mdl.ModelID, perr.Category)
			if !provider.FallbackEligible(perr.Category) || !eligible[string(perr.Category)] {
				out.Err = perr
				out.Model = mdl
				out.Provider = prov
				out.Latency = latency
				return out, nil
			}
			// A billing block will not clear inside a retry window; move to the
			// next account immediately instead of retrying the same one.
			if perr.Category == provider.CatBilling {
				break
			}
			// Bounded exponential backoff with jitter before retrying.
			if r < retries {
				select {
				case <-time.After(jitter(50 * time.Millisecond)):
				case <-ctx.Done():
					out.Err = &provider.Error{Category: provider.CatTimeout, Message: "request cancelled"}
					return out, nil
				}
			}
		}
		if lastErr != nil {
			out.Err = lastErr
			out.Model = mdl
			out.Provider = prov
		}
	}
	if out.Err == nil {
		if lastCooldown != nil {
			out.Err = lastCooldown
		} else {
			out.Err = &provider.Error{Category: provider.CatUnknown, Message: "all route targets failed"}
		}
	}
	return out, nil
}

type singleResult struct {
	Resp       *provider.ChatResponse
	Stream     <-chan provider.StreamEvent
	StatusCode int
}

// cancelWhenDone forwards the provider stream and releases the per-attempt
// context only after the upstream stream ends (or the client goes away).
// Without this, cancelling right after StreamChat returns kills the in-flight
// HTTP response before the first chunk can be proxied.
func cancelWhenDone(ctx context.Context, in <-chan provider.StreamEvent, cancel context.CancelFunc) <-chan provider.StreamEvent {
	out := make(chan provider.StreamEvent)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// attemptOnce performs a single provider call (streaming or not).
func (g *Gateway) attemptOnce(ctx context.Context, prep *PreparedRequest, mdl *model.Model, prov *model.Provider) (*singleResult, *provider.Error) {
	apiKey, err := g.decryptCredential(prov.ID)
	if err != nil {
		return nil, &provider.Error{Category: provider.CatUnknown, Message: "cannot decrypt provider credential", Err: err}
	}
	adapter := g.registry.Get(prov.Type)
	creds := provider.Credentials{APIKey: apiKey, BaseURL: prov.BaseURL}
	req := provider.ChatRequest{
		Model: mdl.ModelID, Messages: prep.Input.Messages, MaxTokens: prep.Input.MaxTokens,
		Temperature: prep.Input.Temperature, TopP: prep.Input.TopP, Stream: prep.Input.Stream,
		System: prep.Input.System, SystemExtra: prep.Input.SystemExtra,
		Headers: prep.Input.Headers, Extra: prep.Input.Extra,
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 1024
	}
	if prep.Input.Stream {
		ch, err := adapter.StreamChat(ctx, creds, mdl.ModelID, req)
		if err != nil {
			return nil, provider.AsError(err)
		}
		return &singleResult{Stream: ch, StatusCode: http.StatusOK}, nil
	}
	resp, err := adapter.Chat(ctx, creds, mdl.ModelID, req)
	if err != nil {
		return nil, provider.AsError(err)
	}
	return &singleResult{Resp: resp, StatusCode: http.StatusOK}, nil
}

func provName(p *model.Provider) string {
	if p == nil {
		return ""
	}
	return p.Name
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func fallbackSet(csv string) map[string]bool {
	if strings.TrimSpace(csv) == "" {
		csv = "rate_limit,timeout,upstream_5xx,network,billing"
	}
	out := map[string]bool{}
	for _, p := range store.SplitCSV(csv) {
		out[p] = true
	}
	return out
}

// errUnauthorized builds a 401.
func errUnauthorized(msg string) error { return &AuthError{Message: msg} }

// AuthError signals an authentication failure at the gateway boundary.
type AuthError struct{ Message string }

func (e *AuthError) Error() string { return e.Message }

// WriteOpenAIError writes an OpenAI-style error body.
func WriteOpenAIError(w http.ResponseWriter, status int, errType, code, message string, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfter > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
}

// jitter returns a randomized backoff duration.
func jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	return base + time.Duration(rand.Int63n(int64(base)))
}

var _ = io.EOF
