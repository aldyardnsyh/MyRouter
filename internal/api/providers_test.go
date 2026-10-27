package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

// providerHarness is a Server on a fresh temp DB with an authenticated admin.
// AllowPrivate is on so base URLs used by the tests need no DNS resolution and
// the tests never touch the network; metadata hosts stay blocked regardless.
type providerHarness struct {
	db  *store.DB
	srv *Server
	h   http.Handler
}

func newProviderHarness(t *testing.T) *providerHarness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "providers.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	box, err := secret.New("test-master-key")
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	user := &model.User{ID: store.NewID("usr"), Username: "admin", PasswordHash: "x", CreatedAt: store.Now()}
	if err := db.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(&model.Session{
		ID: store.HashToken("test-session-value"), UserID: user.ID, CSRFToken: "test-csrf",
		CreatedAt: store.Now(), ExpiresAt: store.Now() + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	s := New(Deps{DB: db, Box: box, Config: ServerConfig{Version: "test", AllowPrivate: true}})
	return &providerHarness{db: db, srv: s, h: s.Handler()}
}

// patch issues an authenticated PATCH with the session cookie and CSRF header.
func (p *providerHarness) patch(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, path, &buf)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session-value"})
	req.Header.Set(csrfHeader, "test-csrf")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

// seedProvider creates a provider with (optionally) a stored credential.
func (p *providerHarness) seedProvider(t *testing.T, baseURL string, withCredential bool) *model.Provider {
	t.Helper()
	prov := &model.Provider{
		ID: store.NewID("prv"), Name: "upstream", Type: model.ProviderCompatible, BaseURL: baseURL,
		Status: model.StatusUnknown, Enabled: true, CreatedAt: store.Now(),
	}
	if err := p.db.CreateProvider(prov); err != nil {
		t.Fatal(err)
	}
	if withCredential {
		box, err := secret.New("test-master-key")
		if err != nil {
			t.Fatal(err)
		}
		ct, nonce, err := box.Encrypt("sk-stored-secret")
		if err != nil {
			t.Fatal(err)
		}
		if err := p.db.SaveCredential(prov.ID, ct, nonce, box.Version(), "sk-****ret"); err != nil {
			t.Fatal(err)
		}
	}
	return prov
}

// TestProviderBaseURLChangeRequiresConfirmation covers SEC-07: repointing a
// provider that already holds a credential must be explicitly confirmed,
// because the stored key would otherwise be sent to the new host.
func TestProviderBaseURLChangeRequiresConfirmation(t *testing.T) {
	h := newProviderHarness(t)

	t.Run("rejected_without_confirmation", func(t *testing.T) {
		prov := h.seedProvider(t, "https://api.old.example/v1", true)
		rec := h.patch(t, "/admin/providers/"+prov.ID, providerPayload{BaseURL: "https://api.new.example/v1"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec); code != "base_url_change_requires_confirmation" {
			t.Fatalf("error code = %q, want base_url_change_requires_confirmation", code)
		}
		// Nothing may be persisted on a rejected request.
		got, err := h.db.ProviderByID(prov.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BaseURL != "https://api.old.example/v1" {
			t.Fatalf("base_url changed despite rejection: %q", got.BaseURL)
		}
		// The credential must still be the original one.
		ct, nonce, err := h.db.CredentialFor(prov.ID)
		if err != nil || len(ct) == 0 || len(nonce) == 0 {
			t.Fatalf("credential lost after rejected request: %v", err)
		}
	})

	t.Run("accepted_with_confirmation", func(t *testing.T) {
		prov := h.seedProvider(t, "https://api.old.example/v1", true)
		rec := h.patch(t, "/admin/providers/"+prov.ID, providerPayload{
			BaseURL: "https://api.new.example/v1/", ConfirmBaseURL: true,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
		}
		got, err := h.db.ProviderByID(prov.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BaseURL != "https://api.new.example/v1" {
			t.Fatalf("base_url = %q, want normalized https://api.new.example/v1", got.BaseURL)
		}
	})

	t.Run("accepted_with_new_api_key", func(t *testing.T) {
		prov := h.seedProvider(t, "https://api.old.example/v1", true)
		rec := h.patch(t, "/admin/providers/"+prov.ID, providerPayload{
			BaseURL: "https://api.new.example/v1", APIKey: "sk-replacement",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
		}
		got, err := h.db.ProviderByID(prov.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BaseURL != "https://api.new.example/v1" {
			t.Fatalf("base_url = %q, want https://api.new.example/v1", got.BaseURL)
		}
	})

	t.Run("no_credential_needs_no_confirmation", func(t *testing.T) {
		prov := h.seedProvider(t, "https://api.old.example/v1", false)
		rec := h.patch(t, "/admin/providers/"+prov.ID, providerPayload{BaseURL: "https://api.new.example/v1"})
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unchanged_url_needs_no_confirmation", func(t *testing.T) {
		prov := h.seedProvider(t, "https://api.old.example/v1", true)
		rec := h.patch(t, "/admin/providers/"+prov.ID, providerPayload{BaseURL: "https://api.old.example/v1/"})
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("ssrf_still_enforced", func(t *testing.T) {
		prov := h.seedProvider(t, "https://api.old.example/v1", true)
		// Even with confirmation and a replacement key, the metadata host is
		// refused before anything is written.
		rec := h.patch(t, "/admin/providers/"+prov.ID, providerPayload{
			BaseURL: "http://169.254.169.254/latest", APIKey: "sk-replacement", ConfirmBaseURL: true,
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec); code != "unsafe_url" {
			t.Fatalf("error code = %q, want unsafe_url", code)
		}
		got, err := h.db.ProviderByID(prov.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BaseURL != "https://api.old.example/v1" {
			t.Fatalf("base_url changed despite SSRF rejection: %q", got.BaseURL)
		}
	})
}
