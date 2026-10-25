package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/myrouter/myrouter/internal/provider"
)

// TestUpdateSettingsAppliesSSRFPolicyLive is the SEC-06 regression test: saving
// allow_private_upstreams must push the new value into the provider SSRF guard
// immediately, without a restart.
func TestUpdateSettingsAppliesSSRFPolicyLive(t *testing.T) {
	prev := provider.AllowPrivate()
	t.Cleanup(func() { provider.SetAllowPrivate(prev) })

	h := newConfigHarness(t)
	provider.SetAllowPrivate(false)

	rec := h.call(t, http.MethodPatch, "/admin/settings", settingsPayload{"allow_private_upstreams": "true"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH settings status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !provider.AllowPrivate() {
		t.Fatal("provider.AllowPrivate() must be true after enabling the setting")
	}

	rec = h.call(t, http.MethodPatch, "/admin/settings", settingsPayload{"allow_private_upstreams": "false"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH settings status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if provider.AllowPrivate() {
		t.Fatal("provider.AllowPrivate() must be false after disabling the setting")
	}

	// The persisted value must also be readable back through the API.
	rec = h.call(t, http.MethodGet, "/admin/settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET settings status = %d", rec.Code)
	}
	var resp struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if got := resp.Data["allow_private_upstreams"]; got != "false" {
		t.Fatalf("persisted allow_private_upstreams = %q, want %q", got, "false")
	}
}

// TestUpdateSettingsRejectsInvalidPort keeps the validation contract intact
// after the handler was touched for SEC-06.
func TestUpdateSettingsRejectsInvalidPort(t *testing.T) {
	h := newConfigHarness(t)
	rec := h.call(t, http.MethodPatch, "/admin/settings", settingsPayload{"api_port": "99999"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
