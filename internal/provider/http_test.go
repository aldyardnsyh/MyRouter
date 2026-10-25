package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDoJSONCapsErrorBody covers SEC-02: a non-2xx upstream body is buffered up
// to 64KB only, so a hostile upstream cannot force unbounded memory use.
func TestDoJSONCapsErrorBody(t *testing.T) {
	const size = 200 << 10 // 200KB, well above the cap
	huge := strings.Repeat("x", size)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(huge))
	}))
	defer srv.Close()

	status, raw, err := doJSON(context.Background(), srv.Client(), http.MethodPost, srv.URL, nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error for a 500 upstream")
	}
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}

	se, ok := err.(*statusError)
	if !ok {
		t.Fatalf("error type = %T, want *statusError", err)
	}
	if len(se.Body) > maxErrorBody {
		t.Fatalf("statusError.Body = %d bytes, want <= %d", len(se.Body), maxErrorBody)
	}
	if len(raw) > maxErrorBody {
		t.Fatalf("returned raw body = %d bytes, want <= %d", len(raw), maxErrorBody)
	}
	// The 300-char truncation in Error() still applies.
	if msg := se.Error(); len(msg) > 400 {
		t.Fatalf("Error() = %d chars, want the truncated message", len(msg))
	}
}

// TestDoJSONKeepsSuccessBudget guards against the cap being applied to 2xx
// responses as well.
func TestDoJSONKeepsSuccessBudget(t *testing.T) {
	payload := `{"pad":"` + strings.Repeat("y", 128<<10) + `"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	var out struct {
		Pad string `json:"pad"`
	}
	status, raw, err := doJSON(context.Background(), srv.Client(), http.MethodPost, srv.URL, nil, nil, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(raw) != len(payload) {
		t.Fatalf("buffered %d bytes, want %d (2xx budget must stay 8MB)", len(raw), len(payload))
	}
	if len(out.Pad) != 128<<10 {
		t.Fatalf("decoded pad = %d bytes", len(out.Pad))
	}
}
