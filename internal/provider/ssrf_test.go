package provider

import (
	"net"
	"net/url"
	"testing"
)

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		allowPrivate bool
		wantErr      bool
	}{
		{"public https", "https://api.openai.com/v1", false, false},
		{"public http ip", "http://93.184.216.34/v1", false, false},
		{"loopback blocked", "http://127.0.0.1:8080/v1", false, true},
		{"loopback allowed", "http://127.0.0.1:8080/v1", true, false},
		{"private range blocked", "http://192.168.1.5/v1", false, true},
		{"private range allowed", "http://192.168.1.5/v1", true, false},
		{"metadata blocked always", "http://169.254.169.254/latest", true, true},
		{"metadata dns blocked always", "http://metadata.google.internal/computeMetadata", true, true},
		{"ftp scheme blocked", "ftp://93.184.216.34", false, true},
		{"javascript scheme blocked", "javascript:alert(1)", false, true},
		{"missing host", "http://", false, true},
		{"userinfo blocked", "http://user:pass@93.184.216.34/v1", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateBaseURL(tc.url, tc.allowPrivate)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.url, err)
			}
		})
	}
}

// TestAllowPrivateRuntimeFlip is the SEC-06 regression test: the SSRF decision
// must follow the live setting, not the value captured when the client was
// built. No real server is needed; the decision helpers are exercised directly.
func TestAllowPrivateRuntimeFlip(t *testing.T) {
	prev := AllowPrivate()
	t.Cleanup(func() { SetAllowPrivate(prev) })

	loopback := []net.IP{net.ParseIP("127.0.0.1")}
	private := []net.IP{net.ParseIP("10.1.2.3")}
	metadata := []net.IP{net.ParseIP("169.254.169.254")}

	// Start from a client built with allowPrivate=false; the transport must
	// still be able to flip later without being rebuilt.
	_ = GuardedClient(false, 0)
	if AllowPrivate() {
		t.Fatal("client built with allowPrivate=false must seed the flag to false")
	}
	if err := checkDialTarget("127.0.0.1", "8080", loopback); err != nil {
		t.Fatalf("loopback on a non-admin port should be allowed: %v", err)
	}
	if err := checkDialTarget("example.internal", "443", private); err == nil {
		t.Fatal("private address must be refused while allow_private_upstreams is off")
	}

	// Same client/flag, new value: private addresses become reachable.
	SetAllowPrivate(true)
	if err := checkDialTarget("example.internal", "443", private); err != nil {
		t.Fatalf("private address must be allowed after enabling the setting: %v", err)
	}
	if err := checkRedirectURL(&url.URL{Scheme: "http", Host: "10.1.2.3:8080"}); err != nil {
		t.Fatalf("redirect to a private host must be allowed after enabling: %v", err)
	}

	// Flipping back must re-block, again without rebuilding anything.
	SetAllowPrivate(false)
	if err := checkDialTarget("example.internal", "443", private); err == nil {
		t.Fatal("private address must be refused again after disabling the setting")
	}
	if err := checkRedirectURL(&url.URL{Scheme: "http", Host: "10.1.2.3:8080"}); err == nil {
		t.Fatal("redirect to a private host must be refused again after disabling")
	}

	// Cloud metadata stays blocked in both states.
	SetAllowPrivate(true)
	if err := checkDialTarget("169.254.169.254", "80", metadata); err == nil {
		t.Fatal("metadata endpoint must be blocked even when private upstreams are enabled")
	}
	if err := checkRedirectURL(&url.URL{Scheme: "http", Host: "169.254.169.254"}); err == nil {
		t.Fatal("redirect to metadata must be blocked even when private upstreams are enabled")
	}
}

// TestDialTargetAdminPortAlwaysBlocked ensures the local admin port is refused
// regardless of the allow_private_upstreams value.
func TestDialTargetAdminPortAlwaysBlocked(t *testing.T) {
	prev := AllowPrivate()
	t.Cleanup(func() { SetAllowPrivate(prev) })

	loopback := []net.IP{net.ParseIP("127.0.0.1")}
	SetAllowPrivate(false)
	if err := checkDialTarget("127.0.0.1", "20128", loopback); err == nil {
		t.Fatal("the admin port must never be reachable through a provider URL")
	}
	if err := checkDialTarget("127.0.0.1", "8080", loopback); err != nil {
		t.Fatalf("benign local port should be allowed: %v", err)
	}
}

func TestIsPrivateIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":   true,
		"10.0.0.5":    true,
		"172.16.0.1":  true,
		"192.168.1.1": true,
		"100.64.0.1":  true, // CGNAT
		"169.254.1.1": true, // link-local
		"8.8.8.8":     false,
		"1.1.1.1":     false,
		"172.32.0.1":  false, // outside 172.16/12
	}
	for ip, want := range cases {
		if got := isPrivateIP(net.ParseIP(ip)); got != want {
			t.Errorf("isPrivateIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestErrorCategories(t *testing.T) {
	if !FallbackEligible(CatRateLimit) || !FallbackEligible(CatTimeout) || !FallbackEligible(CatUpstream5xx) || !FallbackEligible(CatNetwork) || !FallbackEligible(CatBilling) {
		t.Fatal("429/timeout/5xx/network/billing must be fallback eligible")
	}
	if FallbackEligible(CatAuth) || FallbackEligible(CatInvalidRequest) || FallbackEligible(CatNotFound) {
		t.Fatal("auth/invalid/notfound must NOT be fallback eligible")
	}
}

func TestMapErrorStatuses(t *testing.T) {
	adapter := NewOpenAI(nil)
	cases := []struct {
		status   int
		category ErrorCategory
	}{
		{429, CatRateLimit},
		{402, CatBilling},
		{401, CatAuth},
		{403, CatAuth},
		{404, CatNotFound},
		{500, CatUpstream5xx},
		{503, CatUpstream5xx},
		{400, CatInvalidRequest},
	}
	for _, tc := range cases {
		e := adapter.MapError(tc.status, []byte(`{"error":{"message":"boom"}}`))
		if e.Category != tc.category {
			t.Errorf("status %d: got %s, want %s", tc.status, e.Category, tc.category)
		}
	}
}
