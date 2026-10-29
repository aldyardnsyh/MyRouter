package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// AnthropicAdapter implements the Anthropic Messages API wire format
// (https://docs.anthropic.com/en/api/messages).
type AnthropicAdapter struct {
	client *http.Client
}

// NewAnthropic returns the Anthropic adapter.
func NewAnthropic(client *http.Client) *AnthropicAdapter { return &AnthropicAdapter{client: client} }

// Type implements BaseProvider.
func (a *AnthropicAdapter) Type() string { return "anthropic" }

// DefaultBaseURL implements BaseProvider.
func (a *AnthropicAdapter) DefaultBaseURL() string { return "https://api.anthropic.com/v1" }

const anthropicVersion = "2023-06-01"

func (a *AnthropicAdapter) headers(creds Credentials) map[string]string {
	return map[string]string{
		"x-api-key":         creds.APIKey,
		"anthropic-version": anthropicVersion,
	}
}

func (a *AnthropicAdapter) url(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = a.DefaultBaseURL()
	}
	return base + path
}

// Validate implements BaseProvider using the models endpoint.
func (a *AnthropicAdapter) Validate(ctx context.Context, creds Credentials) error {
	_, _, err := doJSON(ctx, a.client, http.MethodGet, a.url(creds.BaseURL, "/models"), a.headers(creds), nil, nil)
	if err != nil {
		return MapHTTPError(err, a.MapError)
	}
	return nil
}

type anthropicModels struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

// ListModels implements BaseProvider.
func (a *AnthropicAdapter) ListModels(ctx context.Context, creds Credentials) ([]ModelInfo, error) {
	var resp anthropicModels
	status, raw, err := doJSON(ctx, a.client, http.MethodGet, a.url(creds.BaseURL, "/models?limit=100"), a.headers(creds), nil, &resp)
	if err != nil {
		if status != 0 {
			return nil, a.MapError(status, raw)
		}
		return nil, classifyTransport(err)
	}
	out := make([]ModelInfo, 0, len(resp.Data))
	for _, m := range resp.Data {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out = append(out, ModelInfo{ModelID: m.ID, DisplayName: name, ContextWindow: 200000, Capabilities: []string{"chat", "tools"}})
	}
	if len(out) == 0 {
		out = []ModelInfo{
			{ModelID: "claude-3-7-sonnet-latest", DisplayName: "Claude 3.7 Sonnet", ContextWindow: 200000, Capabilities: []string{"chat", "tools", "vision"}},
			{ModelID: "claude-3-5-haiku-latest", DisplayName: "Claude 3.5 Haiku", ContextWindow: 200000, Capabilities: []string{"chat", "tools"}},
		}
	}
	return out, nil
}

// anthropicBody converts a normalized request into the Messages payload.
func anthropicBody(model string, req ChatRequest, stream bool) map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages))
	// Anthropic requires user/assistant turns; system moves to a top-level field.
	for _, m := range req.Messages {
		role := m.Role
		if role == "system" {
			continue
		}
		if role != "assistant" {
			role = "user"
		}
		messages = append(messages, map[string]any{"role": role, "content": m.Content})
	}
	body := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": 4096,
		"stream":     stream,
	}
	system := strings.TrimSpace(req.SystemExtra)
	if s := strings.TrimSpace(req.System); s != "" {
		if system != "" {
			system += "\n\n"
		}
		system += s
	}
	if system != "" {
		// Stable prefix (skills/SystemExtra) first + cache breakpoint so
		// repeated requests hit Anthropic prompt caching.
		body["system"] = []any{map[string]any{
			"type": "text", "text": system,
			"cache_control": map[string]any{"type": "ephemeral"},
		}}
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	// Adaptive reasoning budget: extended thinking. Only sent when the
	// client/model opts in via thinking capability — never force it, an
	// unknown field breaks non-thinking models.
	if req.Effort != "" {
		if v, ok := req.Extra["thinking"]; ok {
			body["thinking"] = v
		} else if v, ok := req.Extra["thinking_budget_tokens"]; ok {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": v}
		}
	}
	return body
}

type anthropicResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Chat implements BaseProvider.
func (a *AnthropicAdapter) Chat(ctx context.Context, creds Credentials, model string, req ChatRequest) (*ChatResponse, error) {
	var resp anthropicResponse
	status, raw, err := doJSON(ctx, a.client, http.MethodPost, a.url(creds.BaseURL, "/messages"), a.headers(creds), anthropicBody(model, req, false), &resp)
	if err != nil {
		if status != 0 {
			return nil, a.MapError(status, raw)
		}
		return nil, classifyTransport(err)
	}
	var sb strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return &ChatResponse{
		ID: resp.ID, Model: resp.Model, Content: sb.String(), Finish: resp.StopReason,
		Usage: Usage{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			TotalTokens: resp.Usage.InputTokens + resp.Usage.OutputTokens,
			CachedTokens: resp.Usage.CacheReadInputTokens},
	}, nil
}

// StreamChat implements BaseProvider.
func (a *AnthropicAdapter) StreamChat(ctx context.Context, creds Credentials, model string, req ChatRequest) (<-chan StreamEvent, error) {
	body, _, _, err := postStream(ctx, a.client, a.url(creds.BaseURL, "/messages"), a.headers(creds), anthropicBody(model, req, true))
	if err != nil {
		var se *statusError
		if errors.As(err, &se) {
			return nil, a.MapError(se.Status, se.Body)
		}
		return nil, classifyTransport(err)
	}
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		defer body.Close()
		reader := newSSEReader(body)
		inputTokens, outputTokens := 0, 0
		for {
			payload, err := reader.next()
			if err != nil {
				if ctx.Err() != nil {
					ch <- StreamEvent{Err: &Error{Category: CatTimeout, Message: "stream deadline exceeded", Err: ctx.Err()}}
				}
				return
			}
			var ev struct {
				Type  string `json:"type"`
				Delta struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Message *struct {
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
				Usage *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
				Error *struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			if ev.Error != nil {
				ch <- StreamEvent{Err: &Error{Category: CatUpstream5xx, Message: ev.Error.Message}}
				return
			}
			switch ev.Type {
			case "message_start":
				if ev.Message != nil {
					inputTokens = ev.Message.Usage.InputTokens
				}
			case "content_block_delta":
				if ev.Delta.Text != "" {
					ch <- StreamEvent{Delta: ev.Delta.Text}
				}
			case "message_delta":
				if ev.Usage != nil {
					outputTokens = ev.Usage.OutputTokens
				}
				stop := ev.Delta.StopReason
				ch <- StreamEvent{Finish: stop, InputTok: inputTokens, OutputTok: outputTokens}
			}
		}
	}()
	return ch, nil
}

// MapError implements BaseProvider.
func (a *AnthropicAdapter) MapError(status int, body []byte) *Error {
	msg := errorMessage(status, extractMessage(body))
	switch {
	case status == http.StatusTooManyRequests:
		return &Error{Category: CatRateLimit, StatusCode: status, Message: msg}
	case status == http.StatusPaymentRequired:
		return &Error{Category: CatBilling, StatusCode: status, Message: msg}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Category: CatAuth, StatusCode: status, Message: msg}
	case status == http.StatusNotFound:
		return &Error{Category: CatNotFound, StatusCode: status, Message: msg}
	case status >= 500 || status == 529: // 529 = overloaded
		return &Error{Category: CatUpstream5xx, StatusCode: status, Message: msg}
	case status >= 400:
		return &Error{Category: CatInvalidRequest, StatusCode: status, Message: msg}
	}
	return &Error{Category: CatUnknown, StatusCode: status, Message: msg}
}
