// Package provider defines the provider-agnostic adapter contract. All
// provider-specific behavior lives behind BaseProvider so the routing engine
// never branches on provider type (PRD §11 A2/A3).
package provider

import (
	"context"
	"errors"
	"net/http"
)

// ErrorCategory is the normalized failure taxonomy used by fallback logic.
type ErrorCategory string

const (
	// CatRateLimit: upstream returned 429 (fallback eligible).
	CatRateLimit ErrorCategory = "rate_limit"
	// CatTimeout: deadline exceeded (fallback eligible).
	CatTimeout ErrorCategory = "timeout"
	// CatUpstream5xx: upstream 5xx (fallback eligible).
	CatUpstream5xx ErrorCategory = "upstream_5xx"
	// CatNetwork: dial/DNS/TLS failure (fallback eligible).
	CatNetwork ErrorCategory = "network"
	// CatTimeOut aliases CatTimeout for readability in status mapping.
	CatTimeOut ErrorCategory = CatTimeout
	// CatInvalidRequest: upstream rejected the payload (400/422) — NOT eligible.
	CatInvalidRequest ErrorCategory = "invalid_request"
	// CatAuth: upstream rejected our credential (401/403) — NOT eligible.
	CatAuth ErrorCategory = "auth"
	// CatNotFound: unknown model on upstream — NOT eligible.
	CatNotFound ErrorCategory = "not_found"
	// CatUnknown: anything else.
	CatUnknown ErrorCategory = "unknown"
)

// FallbackEligible reports whether an error category should trigger fallback.
func FallbackEligible(c ErrorCategory) bool {
	switch c {
	case CatRateLimit, CatTimeout, CatUpstream5xx, CatNetwork:
		return true
	}
	return false
}

// Error is a normalized provider failure.
type Error struct {
	Category   ErrorCategory
	StatusCode int
	Message    string
	RetryAfter int // seconds, when the upstream supplied it
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return string(e.Category) + ": " + e.Message + ": " + e.Err.Error()
	}
	return string(e.Category) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// AsError converts any error into a *Error.
func AsError(err error) *Error {
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	return &Error{Category: CatUnknown, Message: err.Error(), Err: err}
}

// ChatMessage is a provider-neutral chat message.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the normalized inbound request.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	System      string        `json:"system,omitempty"`
	// Extra holds any additional fields the client sent that the adapter may
	// forward (stop, presence_penalty, ...). Kept raw on purpose.
	Extra map[string]any `json:"-"`
}

// Usage reports token accounting.
type Usage struct {
	InputTokens  int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ChatResponse is a normalized non-streaming completion.
type ChatResponse struct {
	ID      string
	Model   string
	Content string
	Finish  string
	Usage   Usage
}

// StreamEvent is one normalized streaming chunk.
type StreamEvent struct {
	Delta     string
	Finish    string
	InputTok  int
	OutputTok int
	Err       error
}

// ModelInfo describes an upstream model.
type ModelInfo struct {
	ModelID       string
	DisplayName   string
	ContextWindow int
	PriceIn       *float64
	PriceOut      *float64
	Capabilities  []string
}

// Credentials holds decrypted provider settings.
type Credentials struct {
	APIKey  string
	BaseURL string
}

// BaseProvider is implemented by every adapter.
type BaseProvider interface {
	// Type returns the provider type identifier.
	Type() string
	// DefaultBaseURL returns the canonical API base URL.
	DefaultBaseURL() string
	// Validate checks credentials by performing a cheap authenticated call.
	Validate(ctx context.Context, creds Credentials) error
	// ListModels discovers models available to the credential.
	ListModels(ctx context.Context, creds Credentials) ([]ModelInfo, error)
	// Chat performs a non-streaming completion.
	Chat(ctx context.Context, creds Credentials, model string, req ChatRequest) (*ChatResponse, error)
	// StreamChat streams a completion. The channel is closed when the stream
	// ends; a single terminal event carries Err on failure.
	StreamChat(ctx context.Context, creds Credentials, model string, req ChatRequest) (<-chan StreamEvent, error)
	// MapError maps an HTTP status/body to a normalized error category.
	MapError(status int, body []byte) *Error
}

// Registry resolves adapters by provider type.
type Registry struct {
	byType map[string]BaseProvider
	client *http.Client
}

// NewRegistry builds the adapter registry.
func NewRegistry(client *http.Client) *Registry {
	if client == nil {
		client = &http.Client{}
	}
	r := &Registry{byType: map[string]BaseProvider{}, client: client}
	r.Register(NewOpenAI(client))
	r.Register(NewDeepSeek(client))
	r.Register(NewCompatible(client))
	r.Register(NewAnthropic(client))
	r.Register(NewGemini(client))
	return r
}

// Register adds an adapter.
func (r *Registry) Register(p BaseProvider) { r.byType[p.Type()] = p }

// Get returns the adapter for a provider type, defaulting to the
// OpenAI-compatible adapter for unknown types.
func (r *Registry) Get(providerType string) BaseProvider {
	if p, ok := r.byType[providerType]; ok {
		return p
	}
	return r.byType["openai-compatible"]
}

// DefaultBaseURL returns the canonical base URL for a provider type.
func (r *Registry) DefaultBaseURL(providerType string) string {
	return r.Get(providerType).DefaultBaseURL()
}
