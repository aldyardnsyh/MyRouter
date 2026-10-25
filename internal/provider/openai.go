package provider

import (
	"fmt"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
)

// OpenAIAdapter implements the OpenAI Chat Completions wire format. DeepSeek
// and generic OpenAI-compatible providers reuse it with different defaults.
type OpenAIAdapter struct {
	client   *http.Client
	typeName string
	baseURL  string
	pathChat string
	pathMod  string
	// Models is an optional static catalog for providers without /models.
	catalog []ModelInfo
	prices  map[string][2]float64
}

// NewOpenAI returns the OpenAI adapter.
func NewOpenAI(client *http.Client) *OpenAIAdapter {
	return &OpenAIAdapter{
		client: client, typeName: "openai", baseURL: "https://api.openai.com/v1",
		pathChat: "/chat/completions", pathMod: "/models",
		catalog: []ModelInfo{
			{ModelID: "gpt-4o", DisplayName: "GPT-4o", ContextWindow: 128000, Capabilities: []string{"chat", "vision", "tools"}},
			{ModelID: "gpt-4o-mini", DisplayName: "GPT-4o mini", ContextWindow: 128000, Capabilities: []string{"chat", "vision", "tools"}},
			{ModelID: "gpt-4.1", DisplayName: "GPT-4.1", ContextWindow: 1000000, Capabilities: []string{"chat", "tools"}},
		},
	}
}

// NewDeepSeek returns the DeepSeek adapter (OpenAI-compatible wire format).
func NewDeepSeek(client *http.Client) *OpenAIAdapter {
	return &OpenAIAdapter{
		client: client, typeName: "deepseek", baseURL: "https://api.deepseek.com/v1",
		pathChat: "/chat/completions", pathMod: "/models",
		catalog: []ModelInfo{
			{ModelID: "deepseek-chat", DisplayName: "DeepSeek Chat", ContextWindow: 64000, Capabilities: []string{"chat", "tools"}},
			{ModelID: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner", ContextWindow: 64000, Capabilities: []string{"chat"}},
		},
	}
}

// NewCompatible returns a generic OpenAI-compatible adapter.
func NewCompatible(client *http.Client) *OpenAIAdapter {
	return &OpenAIAdapter{
		client: client, typeName: "openai-compatible", baseURL: "http://localhost:11434/v1",
		pathChat: "/chat/completions", pathMod: "/models",
	}
}

// Type implements BaseProvider.
func (a *OpenAIAdapter) Type() string { return a.typeName }

// DefaultBaseURL implements BaseProvider.
func (a *OpenAIAdapter) DefaultBaseURL() string { return a.baseURL }

func (a *OpenAIAdapter) url(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = a.baseURL
	}
	return base + path
}

func bearer(creds Credentials) map[string]string {
	h := map[string]string{}
	if creds.APIKey != "" {
		h["Authorization"] = "Bearer " + creds.APIKey
	}
	return h
}

// openAIChatBody builds the wire payload.
func (a *OpenAIAdapter) openAIChatBody(model string, req ChatRequest, stream bool) map[string]any {
	body := map[string]any{"model": model, "messages": req.Messages, "stream": stream}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	for k, v := range req.Extra {
		if _, exists := body[k]; !exists {
			body[k] = v
		}
	}
	return body
}

// Validate performs a lightweight authenticated /models call.
func (a *OpenAIAdapter) Validate(ctx context.Context, creds Credentials) error {
	_, _, err := doJSON(ctx, a.client, http.MethodGet, a.url(creds.BaseURL, a.pathMod), bearer(creds), nil, nil)
	if err != nil {
		return MapHTTPError(err, a.MapError)
	}
	return nil
}

type openAIModels struct {
	Data []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// ListModels implements BaseProvider.
func (a *OpenAIAdapter) ListModels(ctx context.Context, creds Credentials) ([]ModelInfo, error) {
	var resp openAIModels
	status, raw, err := doJSON(ctx, a.client, http.MethodGet, a.url(creds.BaseURL, a.pathMod), bearer(creds), nil, &resp)
	if err != nil {
		if status != 0 {
			return nil, a.MapError(status, raw)
		}
		return nil, classifyTransport(err)
	}
	out := make([]ModelInfo, 0, len(resp.Data))
	for _, m := range resp.Data {
		out = append(out, ModelInfo{ModelID: m.ID, DisplayName: m.ID, Capabilities: []string{"chat"}})
	}
	if len(out) == 0 {
		// Static catalog fallback keeps the UI usable on providers that hide /models.
		out = append(out, a.catalog...)
	}
	return out, nil
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// Chat implements BaseProvider.
func (a *OpenAIAdapter) Chat(ctx context.Context, creds Credentials, model string, req ChatRequest) (*ChatResponse, error) {
	var resp openAIChatResponse
	status, raw, err := doJSON(ctx, a.client, http.MethodPost, a.url(creds.BaseURL, a.pathChat), bearer(creds), a.openAIChatBody(model, req, false), &resp)
	if err != nil {
		if status != 0 {
			return nil, a.MapError(status, raw)
		}
		return nil, classifyTransport(err)
	}
	out := &ChatResponse{ID: resp.ID, Model: resp.Model, Usage: resp.Usage}
	if len(resp.Choices) > 0 {
		out.Content = resp.Choices[0].Message.Content
		out.Finish = resp.Choices[0].FinishReason
	}
	if out.Usage.TotalTokens == 0 {
		out.Usage.TotalTokens = out.Usage.InputTokens + out.Usage.OutputTokens
	}
	return out, nil
}

// StreamChat implements BaseProvider.
func (a *OpenAIAdapter) StreamChat(ctx context.Context, creds Credentials, model string, req ChatRequest) (<-chan StreamEvent, error) {
	body, hdr, status, err := postStream(ctx, a.client, a.url(creds.BaseURL, a.pathChat), bearer(creds), a.openAIChatBody(model, req, true))
	if err != nil {
		var se *statusError
		if errors.As(err, &se) {
			return nil, a.MapError(se.Status, se.Body)
		}
		return nil, classifyTransport(err)
	}
	_ = hdr
	_ = status
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		defer body.Close()
		reader := newSSEReader(body)
		for {
			payload, err := reader.next()
			if err != nil {
				if ctx.Err() != nil {
					ch <- StreamEvent{Err: &Error{Category: CatTimeout, Message: "stream deadline exceeded", Err: ctx.Err()}}
				}
				return
			}
			if payload == "[DONE]" {
				return
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *Usage `json:"usage"`
				Error *struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			if chunk.Error != nil {
				ch <- StreamEvent{Err: &Error{Category: CatUpstream5xx, Message: chunk.Error.Message}}
				return
			}
			ev := StreamEvent{}
			for _, c := range chunk.Choices {
				ev.Delta += c.Delta.Content
				ev.Finish = c.FinishReason
			}
			if chunk.Usage != nil {
				ev.InputTok = chunk.Usage.InputTokens
				ev.OutputTok = chunk.Usage.OutputTokens
			}
			ch <- ev
		}
	}()
	return ch, nil
}

// MapError maps HTTP statuses to the normalized taxonomy.
func (a *OpenAIAdapter) MapError(status int, body []byte) *Error {
	msg := extractMessage(body)
	switch {
	case status == http.StatusTooManyRequests:
		return &Error{Category: CatRateLimit, StatusCode: status, Message: msg}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Category: CatAuth, StatusCode: status, Message: msg}
	case status == http.StatusNotFound:
		return &Error{Category: CatNotFound, StatusCode: status, Message: msg}
	case status >= 500:
		return &Error{Category: CatUpstream5xx, StatusCode: status, Message: msg}
	case status >= 400:
		return &Error{Category: CatInvalidRequest, StatusCode: status, Message: msg}
	}
	return &Error{Category: CatUnknown, StatusCode: status, Message: msg}
}

// extractMessage pulls a human-readable message out of an error body.
func extractMessage(body []byte) string {
	var obj struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &obj); err == nil {
		if obj.Error.Message != "" {
			return obj.Error.Message
		}
		if obj.Message != "" {
			return obj.Message
		}
	}
	return truncate(strings.TrimSpace(string(body)), 300)
}

// errorMessage ensures a surfaced upstream error always carries context, even
// when the upstream sent an empty or unparseable error body.
func errorMessage(status int, msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return fmt.Sprintf("upstream returned HTTP %d (no detail)", status)
	}
	return msg
}

// classifyTransport maps Go transport errors into fallback categories.
func classifyTransport(err error) error {
	if err == nil {
		return nil
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Category: CatTimeout, Message: "request timed out", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Category: CatTimeout, Message: "deadline exceeded", Err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Category: CatNetwork, Message: "request cancelled", Err: err}
	}
	return &Error{Category: CatNetwork, Message: "network error", Err: err}
}
