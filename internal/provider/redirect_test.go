package provider

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGuardedClientBlocksRedirectToPrivate ensures an upstream cannot bounce
// the request to an internal host (SSRF via redirect).
func TestGuardedClientBlocksRedirectToPrivate(t *testing.T) {
	// Target that must never be reached.
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("internal server was reached through a redirect")
	}))
	defer internal.Close()

	// Public-facing server that redirects to the internal one.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	client := GuardedClient(false, 0)
	resp, err := client.Get(redirector.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected redirect to a private address to be blocked")
	}
}

// TestGuardedClientBlocksAdminPort verifies the gateway's own admin port can
// never be reached through a provider URL.
func TestGuardedClientBlocksAdminPort(t *testing.T) {
	// Simulate an admin server on an ephemeral port; we cannot bind 20128 here,
	// so assert the guard's port decision directly.
	if isLocalTarget("127.0.0.1", "20128") {
		t.Fatal("port 20128 (admin) must never be treated as a benign local target")
	}
	if !isLocalTarget("127.0.0.1", "8080") {
		t.Fatal("a non-admin local port should be allowed for local providers")
	}
}

// TestGuardedTransportBlocksMetadata ensures cloud metadata is always refused.
func TestGuardedTransportBlocksMetadata(t *testing.T) {
	client := GuardedClient(true, 0) // even with private upstreams enabled
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("metadata endpoint must be blocked even when private upstreams are allowed")
	}
	_ = net.ParseIP
}
