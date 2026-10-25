package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// TestRateLimit429BodyShape drives the real HTTP handler end-to-end with a
// Strict policy (1 RPM) and asserts the 429 body is OpenAI-style JSON with a
// Retry-After header. This is the regression proof for the E2E check
// "429 body is OpenAI-style" which is flaky to read from PowerShell.
func TestRateLimit429BodyShape(t *testing.T) {
	g, db, keySecret := newTestGateway(t, "ok", "ok")

	// Strict-like policy: 1 request per minute, burst 1, auto-block disabled.
	pol := &model.Policy{
		Name: "TestStrict", RPM: 1, Burst: 1,
		MaxIPs: 0, MaxDevices: 0, AutoBlockN: 0, AutoBlockM: 0, CreatedAt: store.Now(),
	}
	if err := db.UpsertPolicy(pol); err != nil {
		t.Fatal(err)
	}
	if err := db.BindPolicy(pol.ID, "api_key", "key1"); err != nil {
		t.Fatal(err)
	}

	h := g.Handler()
	mk := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"coding","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+keySecret)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	first := mk()
	if first.Code != http.StatusOK {
		t.Fatalf("first request: got %d want 200 body=%s", first.Code, first.Body.String())
	}
	second := mk()
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d want 429", second.Code)
	}
	if ra := second.Header().Get("Retry-After"); ra == "" {
		t.Error("429 must include Retry-After header")
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body not valid JSON: %v body=%s", err, second.Body.String())
	}
	if body.Error.Type != "rate_limit_exceeded" || body.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("429 error shape wrong: type=%q code=%q message=%q",
			body.Error.Type, body.Error.Code, body.Error.Message)
	}
	if body.Error.Message == "" {
		t.Error("429 error.message must not be empty")
	}
	if !strings.Contains(body.Error.Message, "1 requests/min") {
		t.Errorf("message should mention limit: %q", body.Error.Message)
	}
	_ = guard.New
	_ = time.Now
}
