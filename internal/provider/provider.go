// Package provider defines the provider-agnostic adapter contract. All
// provider-specific behavior lives behind BaseProvider so the routing engine
// never branches on provider type (PRD §11 A2/A3).
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
	// CatBilling: upstream rejected the account for billing/quota reasons (402)
	// — fallback eligible so another account can serve the request.
	CatBilling ErrorCategory = "billing"
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
	case CatRateLimit, CatTimeout, CatUpstream5xx, CatNetwork, CatBilling:
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
//
// Content is kept as raw JSON because OpenAI-compatible clients send either a
// plain string or an array of typed parts (text, image_url, input_audio).
// ToolCallID is required when Role is "tool": the Responses API needs it as
// function_call_output.call_id, and dropping it makes upstreams reject the
// whole conversation.
type ChatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
}

// Text returns the message content as plain text. Array content is flattened
// by concatenating its text parts, ignoring non-text parts.
func (m ChatMessage) Text() string {
	raw := strings.TrimSpace(string(m.Content))
	if raw == "" || raw == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			return s
		}
		return ""
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// NewTextMessage builds a message with plain string content.
func NewTextMessage(role, content string) ChatMessage {
	b, _ := json.Marshal(content)
	return ChatMessage{Role: role, Content: b}
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
	// Effort is the adaptive reasoning budget (low|medium|high, default
	// medium). Adapters map it to the provider-native field and must drop it
	// when the provider does not support it.
	Effort string `json:"-"`
	// SystemExtra is server-side context added by MyRouter skills. Adapters
	// append it after the client's own system prompt instead of mutating the
	// client's message list.
	SystemExtra string `json:"-"`
	// Headers carries client headers the gateway explicitly forwards to the
	// upstream (OpenCode session/client headers, User-Agent). Not serialized.
	Headers http.Header `json:"-"`
	// Extra holds any additional fields the client sent that the adapter may
	// forward (stop, presence_penalty, ...). Kept raw on purpose.
	Extra map[string]any `json:"-"`
}

// NormalizeEffort validates a route effort value, defaulting to medium.
func NormalizeEffort(e string) string {
	switch e {
	case "low", "medium", "high":
		return e
	default:
		return "medium"
	}
}

// AnthropicThinkingBudget maps effort to an Anthropic extended-thinking
// token budget. Only applied when the model supports thinking.
func AnthropicThinkingBudget(effort string) int {
	switch NormalizeEffort(effort) {
	case "low":
		return 1024
	case "high":
		return 10000
	default:
		return 4096
	}
}

// GeminiThinkingLevel maps effort to the Gemini thinkingLevel value
// (low|medium|high). Only applied when the model supports thinking.
func GeminiThinkingLevel(effort string) string { return NormalizeEffort(effort) }

// OpenAIEffort maps effort to the OpenAI Responses reasoning.effort value.
// Only applied when the model supports reasoning.
func OpenAIEffort(effort string) string { return NormalizeEffort(effort) }

// Usage reports token accounting. CachedTokens is 0 when the upstream
// does not report a cache hit (OpenAI prompt_tokens_details.cached_tokens,
// Anthropic cache_read_input_tokens, Gemini cachedContentTokenCount).
type Usage struct {
	InputTokens  int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
	TotalTokens  int `json:"total_tokens"`
	CachedTokens int `json:"cached_tokens,omitempty"`
	// PromptTokensDetails carries OpenAI implicit-cache detail.
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// ChatResponse is a normalized non-streaming completion.
type ChatResponse struct {
	ID        string
	Model     string
	Content   string
	Reasoning string
	ToolCalls json.RawMessage
	Finish    string
	Usage     Usage
}

// StreamEvent is one normalized streaming chunk.
type StreamEvent struct {
	Delta     string
	Reasoning string
	ToolCalls json.RawMessage
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
