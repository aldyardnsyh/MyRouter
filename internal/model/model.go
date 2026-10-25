// Package model holds the persisted domain entities (mirrors docs/schema.sql).
package model

// Provider types.
const (
	ProviderOpenAI     = "openai"
	ProviderAnthropic  = "anthropic"
	ProviderGemini     = "gemini"
	ProviderDeepSeek   = "deepseek"
	ProviderCompatible = "openai-compatible"
)

// Provider status values (PRD §4 status badges).
const (
	StatusConnected   = "connected"
	StatusDegraded    = "degraded"
	StatusRateLimited = "rate_limited"
	StatusUnavailable = "unavailable"
	StatusInvalidCred = "invalid_credentials"
	StatusUnknown     = "unknown"
)

// User is the single admin account.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
}

// Session is an admin login session.
type Session struct {
	ID        string
	UserID    string
	CSRFToken string
	CreatedAt int64
	ExpiresAt int64
}

// Provider is an upstream LLM API.
type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	BaseURL   string `json:"base_url"`
	Status    string `json:"status"`
	Enabled   bool   `json:"enabled"`
	LastCheck *int64 `json:"last_check"`
	LastError string `json:"last_error"`
	CreatedAt int64  `json:"created_at"`

	// Derived fields (not columns).
	ModelCount int    `json:"model_count"`
	Credential string `json:"credential_hint,omitempty"`
}

// Model is a provider model.
type Model struct {
	ID            string   `json:"id"`
	ProviderID    string   `json:"provider_id"`
	ProviderName  string   `json:"provider_name,omitempty"`
	ModelID       string   `json:"model_id"`
	DisplayName   string   `json:"display_name"`
	ContextWindow int      `json:"context_window"`
	PriceIn       *float64 `json:"price_in"`
	PriceOut      *float64 `json:"price_out"`
	Capabilities  string   `json:"capabilities"`
	Available     bool     `json:"available"`
	Status        string   `json:"status,omitempty"`
}

// RouteTarget is a primary or fallback model of a route.
type RouteTarget struct {
	ID           string `json:"id"`
	RouteID      string `json:"route_id"`
	ModelID      string `json:"model_id"`
	Position     int    `json:"position"`
	Role         string `json:"role"`
	ModelName    string `json:"model_name,omitempty"`
	ProviderID   string `json:"provider_id,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	Available    bool   `json:"available"`
}

// Route is a virtual model alias.
type Route struct {
	ID          string        `json:"id"`
	Alias       string        `json:"alias"`
	Description string        `json:"description"`
	Enabled     bool          `json:"enabled"`
	Strategy    string        `json:"strategy"`
	Retry       int           `json:"retry"`
	TimeoutMS   int           `json:"timeout_ms"`
	PolicyID    *string       `json:"policy_id"`
	PolicyName  string        `json:"policy_name,omitempty"`
	FallbackOn  string        `json:"fallback_on"`
	CreatedAt   int64         `json:"created_at"`
	Targets     []RouteTarget `json:"targets"`
}

// Primary returns the primary target, if any.
func (r Route) Primary() (RouteTarget, bool) {
	for _, t := range r.Targets {
		if t.Role == "primary" {
			return t, true
		}
	}
	if len(r.Targets) > 0 {
		return r.Targets[0], true
	}
	return RouteTarget{}, false
}

// Fallbacks returns the ordered fallback targets.
func (r Route) Fallbacks() []RouteTarget {
	out := make([]RouteTarget, 0, len(r.Targets))
	for _, t := range r.Targets {
		if t.Role == "fallback" {
			out = append(out, t)
		}
	}
	return out
}

// APIKey is a client key. The secret itself is never stored.
type APIKey struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Prefix        string  `json:"prefix"`
	Hash          string  `json:"-"`
	Enabled       bool    `json:"enabled"`
	CreatedAt     int64   `json:"created_at"`
	ExpiresAt     *int64  `json:"expires_at"`
	LastUsedAt    *int64  `json:"last_used_at"`
	PolicyID      *string `json:"policy_id"`
	PolicyName    string  `json:"policy_name,omitempty"`
	AllowedRoutes string  `json:"allowed_routes"`
}

// Policy is a reusable Guard policy.
type Policy struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	RPM           int    `json:"rpm"`
	TPM           int    `json:"tpm"`
	Burst         int    `json:"burst"`
	DailyTokens   int    `json:"daily_tokens"`
	MonthlyTokens int    `json:"monthly_tokens"`
	MaxConcurrent int    `json:"max_concurrent"`
	MaxTokensReq  int    `json:"max_tokens_per_request"`
	MaxIPs        int    `json:"max_ips"`
	MaxDevices    int    `json:"max_devices"`
	AllowedModels string `json:"allowed_models"`
	OnExceed      string `json:"on_exceed"`
	QueueMaxS     int    `json:"queue_max_s"`
	CooldownS     int    `json:"cooldown_s"`
	AutoBlockN    int    `json:"auto_block_n"`
	AutoBlockM    int    `json:"auto_block_m"`
	AutoBlockS    int    `json:"auto_block_s"`
	CreatedAt     int64  `json:"created_at"`

	BoundCount int `json:"bound_count"`
}

// PolicyBinding attaches a policy to a scope object.
type PolicyBinding struct {
	ID        string `json:"id"`
	PolicyID  string `json:"policy_id"`
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
}

// IPRule is an allow/deny CIDR entry.
type IPRule struct {
	ID        string `json:"id"`
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	ScopeName string `json:"scope_name,omitempty"`
	CIDR      string `json:"cidr"`
	Action    string `json:"action"`
	Note      string `json:"note"`
	ExpiresAt *int64 `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
}

// Device is a soft-identified client device.
type Device struct {
	ID          string `json:"id"`
	APIKeyID    string `json:"api_key_id"`
	APIKeyName  string `json:"api_key_name,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
	LastIP      string `json:"last_ip"`
	UserAgent   string `json:"user_agent"`
	FirstSeen   int64  `json:"first_seen"`
	LastSeen    int64  `json:"last_seen"`
	Blocked     bool   `json:"blocked"`
	Requests    int64  `json:"requests"`
}

// BlockEntry is an active IP/device/key block.
type BlockEntry struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	ScopeID   string `json:"scope_id"`
	Reason    string `json:"reason"`
	Until     *int64 `json:"until"`
	CreatedAt int64  `json:"created_at"`
}

// TraceStep is one routing attempt recorded in request_log.trace.
type TraceStep struct {
	Attempt    int    `json:"attempt"`
	Target     string `json:"target"`
	Provider   string `json:"provider"`
	Outcome    string `json:"outcome"` // success|error
	Category   string `json:"category,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	LatencyMS  int64  `json:"latency_ms"`
	Message    string `json:"message,omitempty"`
}

// RequestLog is an activity record.
type RequestLog struct {
	ID             string      `json:"id"`
	RequestID      string      `json:"request_id"`
	TS             int64       `json:"ts"`
	RouteID        string      `json:"route_id"`
	RouteAlias     string      `json:"route_alias,omitempty"`
	RequestedModel string      `json:"requested_model"`
	ModelID        string      `json:"model_id"`
	ModelName      string      `json:"model_name,omitempty"`
	ProviderID     string      `json:"provider_id"`
	ProviderName   string      `json:"provider_name,omitempty"`
	APIKeyID       string      `json:"api_key_id"`
	APIKeyName     string      `json:"api_key_name,omitempty"`
	APIKeyPrefix   string      `json:"api_key_prefix,omitempty"`
	IP             string      `json:"ip"`
	DeviceID       string      `json:"device_id"`
	Status         string      `json:"status"`
	HTTPStatus     int         `json:"http_status"`
	LatencyMS      int64       `json:"latency_ms"`
	InTokens       int         `json:"in_tokens"`
	OutTokens      int         `json:"out_tokens"`
	FallbackFrom   string      `json:"fallback_from"`
	ErrorCode      string      `json:"error_code"`
	GuardReason    string      `json:"guard_reason"`
	Trace          []TraceStep `json:"trace"`
}

// UsageRow is an aggregate usage record.
type UsageRow struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Requests  int64    `json:"requests"`
	OK        int64    `json:"ok"`
	Failed    int64    `json:"failed"`
	Fallbacks int64    `json:"fallbacks"`
	InTokens  int64    `json:"in_tokens"`
	OutTokens int64    `json:"out_tokens"`
	EstCost   *float64 `json:"est_cost"`
}
