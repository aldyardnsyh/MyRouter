package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestValidateMapsHTTPStatuses ensures Validate normalizes upstream statuses
// into the correct error category (regression: non-2xx used to be reported as
// a network error, so providers were marked "unavailable" instead of
// "invalid_credentials" / "rate_limited").
func TestValidateMapsHTTPStatuses(t *testing.T) {
	cases := []struct {
		status   int
		category ErrorCategory
	}{
		{http.StatusUnauthorized, CatAuth},
		{http.StatusForbidden, CatAuth},
		{http.StatusTooManyRequests, CatRateLimit},
		{http.StatusNotFound, CatNotFound},
		{http.StatusInternalServerError, CatUpstream5xx},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"message":"boom"}}`)
			}))
			defer srv.Close()

			a := NewCompatible(nil)
			err := a.Validate(context.Background(), Credentials{BaseURL: srv.URL, APIKey: "k"})
			if err == nil {
				t.Fatalf("expected error for status %d", tc.status)
			}
			pe := AsError(err)
			if pe.Category != tc.category {
				t.Fatalf("status %d: category = %s, want %s", tc.status, pe.Category, tc.category)
			}
		})
	}
}

// TestValidateSuccess verifies a 200 yields no error.
func TestValidateSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	a := NewCompatible(nil)
	if err := a.Validate(context.Background(), Credentials{BaseURL: srv.URL, APIKey: "k"}); err != nil {
		t.Fatalf("expected success: %v", err)
	}
}
