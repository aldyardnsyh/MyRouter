package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GeminiAdapter implements the Google Generative Language API
// (https://ai.google.dev/api/generate-content).
type GeminiAdapter struct {
	client *http.Client
}

// NewGemini returns the Gemini adapter.
func NewGemini(client *http.Client) *GeminiAdapter { return &GeminiAdapter{client: client} }

// Type implements BaseProvider.
func (a *GeminiAdapter) Type() string { return "gemini" }

// DefaultBaseURL implements BaseProvider.
func (a *GeminiAdapter) DefaultBaseURL() string {
	return "https://generativelanguage.googleapis.com/v1beta"
}

func (a *GeminiAdapter) url(baseURL, path, apiKey string) string {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = a.DefaultBaseURL()
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%s%sx-goog-api-key=%s", base, path, sep, url.QueryEscape(apiKey))
}

// Validate implements BaseProvider.
func (a *GeminiAdapter) Validate(ctx context.Context, creds Credentials) error {
	_, _, err := doJSON(ctx, a.client, http.MethodGet, a.url(creds.BaseURL, "/models", creds.APIKey), nil, nil, nil)
	if err != nil {
		return MapHTTPError(err, a.MapError)
	}
	return nil
}

type geminiModels struct {
	Models []struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		InputLimit  int    `json:"inputTokenLimit"`
	} `json:"models"`
}

// ListModels implements BaseProvider.
func (a *GeminiAdapter) ListModels(ctx context.Context, creds Credentials) ([]ModelInfo, error) {
	var resp geminiModels
	status, raw, err := doJSON(ctx, a.client, http.MethodGet, a.url(creds.BaseURL, "/models?pageSize=200", creds.APIKey), nil, nil, &resp)
	if err != nil {
		if status != 0 {
			return nil, a.MapError(status, raw)
		}
		return nil, classifyTransport(err)
	}
	out := make([]ModelInfo, 0, len(resp.Models))
	for _, m := range resp.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if strings.Contains(id, "embedding") || strings.Contains(id, "aqa") {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = id
		}
		out = append(out, ModelInfo{ModelID: id, DisplayName: name, ContextWindow: m.InputLimit, Capabilities: []string{"chat", "vision"}})
	}
	if len(out) == 0 {
		out = []ModelInfo{
			{ModelID: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash", ContextWindow: 1048576, Capabilities: []string{"chat", "vision"}},
			{ModelID: "gemini-1.5-pro", DisplayName: "Gemini 1.5 Pro", ContextWindow: 2097152, Capabilities: []string{"chat", "vision"}},
		}
	}
	return out, nil
}

// geminiBody converts a normalized request into the generateContent payload.
func geminiBody(req ChatRequest) map[string]any {
	var contents []map[string]any
	systemText := req.System
	if extra := strings.TrimSpace(req.SystemExtra); extra != "" {
		if systemText != "" {
			systemText += "\n\n"
		}
		systemText += extra
	}
	for _, m := range req.Messages {
		if m.Role == "system" {
			if systemText != "" {
				systemText += "\n\n"
			}
			systemText += m.Text()
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": []map[string]any{{"text": m.Content}},
		})
	}
	body := map[string]any{"contents": contents}
	if systemText != "" {
		body["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": systemText}}}
	}
	gen := map[string]any{}
	if req.Temperature != nil {
		gen["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		gen["topP"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		gen["maxOutputTokens"] = req.MaxTokens
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}
	return body
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// Chat implements BaseProvider.
func (a *GeminiAdapter) Chat(ctx context.Context, creds Credentials, model string, req ChatRequest) (*ChatResponse, error) {
	var resp geminiResponse
	path := "/models/" + url.PathEscape(model) + ":generateContent"
	status, raw, err := doJSON(ctx, a.client, http.MethodPost, a.url(creds.BaseURL, path, creds.APIKey), nil, geminiBody(req), &resp)
	if err != nil {
		if status != 0 {
			return nil, a.MapError(status, raw)
		}
		return nil, classifyTransport(err)
	}
	var sb strings.Builder
	finish := ""
	for _, c := range resp.Candidates {
		for _, p := range c.Content.Parts {
			sb.WriteString(p.Text)
		}
		finish = strings.ToLower(c.FinishReason)
	}
	return &ChatResponse{
		Model: model, Content: sb.String(), Finish: finish,
		Usage: Usage{InputTokens: resp.UsageMetadata.PromptTokenCount, OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens: resp.UsageMetadata.TotalTokenCount},
	}, nil
}

// StreamChat implements BaseProvider using streamGenerateContent&alt=sse.
func (a *GeminiAdapter) StreamChat(ctx context.Context, creds Credentials, model string, req ChatRequest) (<-chan StreamEvent, error) {
	path := "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
	body, _, _, err := postStream(ctx, a.client, a.url(creds.BaseURL, path, creds.APIKey), nil, geminiBody(req))
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
		for {
			payload, err := reader.next()
			if err != nil {
				if ctx.Err() != nil {
					ch <- StreamEvent{Err: &Error{Category: CatTimeout, Message: "stream deadline exceeded", Err: ctx.Err()}}
				}
				return
			}
			var chunk geminiResponse
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			ev := StreamEvent{InputTok: chunk.UsageMetadata.PromptTokenCount, OutputTok: chunk.UsageMetadata.CandidatesTokenCount}
			for _, c := range chunk.Candidates {
				for _, p := range c.Content.Parts {
					ev.Delta += p.Text
				}
				ev.Finish = strings.ToLower(c.FinishReason)
			}
			ch <- ev
		}
	}()
	return ch, nil
}

// MapError implements BaseProvider.
func (a *GeminiAdapter) MapError(status int, body []byte) *Error {
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
	case status >= 500:
		return &Error{Category: CatUpstream5xx, StatusCode: status, Message: msg}
	case status >= 400:
		return &Error{Category: CatInvalidRequest, StatusCode: status, Message: msg}
	}
	return &Error{Category: CatUnknown, StatusCode: status, Message: msg}
}
