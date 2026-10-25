package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// authHarness is a Server on a fresh temp DB, optionally with an admin whose
// password is known to the test.
type authHarness struct {
	db  *store.DB
	srv *Server
	h   http.Handler
	now time.Time
}

// newAuthHarness builds an isolated server. When withAdmin is true an "admin"
// user with password adminPassword exists.
func newAuthHarness(t *testing.T, withAdmin bool) *authHarness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if withAdmin {
		// MinCost keeps the login tests fast; the production path still uses
		// bcrypt.DefaultCost.
		hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CreateUser(&model.User{
			ID: store.NewID("usr"), Username: "admin", PasswordHash: string(hash), CreatedAt: store.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	srv := New(Deps{DB: db, Config: ServerConfig{Version: "test"}})
	a := &authHarness{db: db, srv: srv, h: srv.Handler(), now: time.Now()}
	// Freeze the limiter clock so tests can jump past the window instantly.
	srv.setClock(func() time.Time { return a.now })
	return a
}

const adminPassword = "correct-horse-battery"

// advance moves the injected clock forward.
func (a *authHarness) advance(d time.Duration) { a.now = a.now.Add(d) }

// post issues a JSON POST from a fixed client IP.
func (a *authHarness) post(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.RemoteAddr = "203.0.113.7:54321"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func (a *authHarness) login(t *testing.T, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	return a.post(t, "/admin/auth/login", loginRequest{Username: username, Password: password})
}

// errorCode extracts error.code from a response body.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return out.Error.Code
}

// TestLoginRateLimitPerIP hammers the login endpoint with wrong passwords from
// one IP and expects the 11th attempt to be rejected with 429 + Retry-After.
// Even a correct password must stay blocked until the window elapses.
func TestLoginRateLimitPerIP(t *testing.T) {
	a := newAuthHarness(t, true)

	// The per-username budget is 5, so each attempt uses a distinct username to
	// isolate the per-IP budget of 10.
	for i := 0; i < maxFailuresPerIP; i++ {
		rec := a.login(t, fmt.Sprintf("ghost-%d", i), "wrong-password")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	rec := a.login(t, "ghost-final", "wrong-password")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d: got %d, want 429: %s", maxFailuresPerIP+1, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("429 response must carry a Retry-After header")
	}
	if code := errorCode(t, rec); code != "too_many_attempts" {
		t.Fatalf("error code = %q, want too_many_attempts", code)
	}

	// Valid credentials do not bypass an active lockout.
	if rec := a.login(t, "admin", adminPassword); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("valid login during lockout: got %d, want 429", rec.Code)
	}

	// Once the window slides past the recorded failures the login works again.
	a.advance(attemptWindow + time.Second)
	if rec := a.login(t, "admin", adminPassword); rec.Code != http.StatusOK {
		t.Fatalf("login after window: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// TestLoginRateLimitPerUsername checks the tighter per-username budget: five
// failures lock the username even when the attempts come from different IPs.
func TestLoginRateLimitPerUsername(t *testing.T) {
	a := newAuthHarness(t, true)

	loginFrom := func(remoteAddr, username, password string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(loginRequest{Username: username, Password: password})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/admin/auth/login", strings.NewReader(string(raw)))
		req.RemoteAddr = remoteAddr
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < maxFailuresPerUser; i++ {
		// Rotate IPs so only the username counter can trip.
		ip := fmt.Sprintf("198.51.100.%d:1234", i+1)
		if rec := loginFrom(ip, "admin", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, rec.Code)
		}
	}

	rec := loginFrom("198.51.100.99:1234", "admin", adminPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: got %d, want 429: %s", maxFailuresPerUser, rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 response must carry a Retry-After header")
	}
	if code := errorCode(t, rec); code != "too_many_attempts" {
		t.Fatalf("error code = %q, want too_many_attempts", code)
	}
}

// TestLoginSuccessResetsCounter verifies a successful login clears both the IP
// and username failure history.
func TestLoginSuccessResetsCounter(t *testing.T) {
	a := newAuthHarness(t, true)

	// Four failures: one short of the per-username budget of five.
	for i := 0; i < maxFailuresPerUser-1; i++ {
		if rec := a.login(t, "admin", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("warm-up %d: got %d, want 401", i+1, rec.Code)
		}
	}
	if rec := a.login(t, "admin", adminPassword); rec.Code != http.StatusOK {
		t.Fatalf("valid login: got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// If the counter had not been reset, these extra failures would push the
	// username past five and answer 429 instead of 401.
	for i := 0; i < maxFailuresPerUser-1; i++ {
		rec := a.login(t, "admin", "wrong-password")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset attempt %d: got %d, want 401 (counter was not reset): %s",
				i+1, rec.Code, rec.Body.String())
		}
	}
}

// TestSetupCompleteIsIdempotent verifies the second setup call is refused with
// 409 and does not create a second admin.
func TestSetupCompleteIsIdempotent(t *testing.T) {
	a := newAuthHarness(t, false)
	token := a.srv.currentSetupToken()

	first := a.post(t, "/admin/setup/complete",
		setupRequest{Username: "root", Password: "supersecret1", SetupToken: token})
	if first.Code != http.StatusOK {
		t.Fatalf("first setup: got %d, want 200: %s", first.Code, first.Body.String())
	}

	second := a.post(t, "/admin/setup/complete",
		setupRequest{Username: "other", Password: "supersecret2", SetupToken: token})
	if second.Code != http.StatusConflict {
		t.Fatalf("second setup: got %d, want 409: %s", second.Code, second.Body.String())
	}
	if code := errorCode(t, second); code != "setup_already_complete" {
		t.Fatalf("error code = %q, want setup_already_complete", code)
	}

	n, err := a.db.UserCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("user count = %d, want 1", n)
	}
	// The original admin must be untouched.
	u, err := a.db.UserByUsername("root")
	if err != nil {
		t.Fatalf("first admin missing: %v", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("supersecret1")) != nil {
		t.Fatal("first admin password was modified")
	}
}

// postFrom issues a JSON POST from an explicit client address.
func (a *authHarness) postFrom(t *testing.T, remoteAddr, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// TestSetupRequiresTokenFromNonLoopback covers SEC-01: a remote caller cannot
// bootstrap the first admin without the token that was logged at startup.
func TestSetupRequiresTokenFromNonLoopback(t *testing.T) {
	a := newAuthHarness(t, false)
	if tok := a.srv.currentSetupToken(); tok == "" {
		t.Fatal("a first-run server must have a setup token")
	} else if !strings.HasPrefix(tok, "mr-setup-") || len(tok) != len("mr-setup-")+32 {
		// initSetupToken uses randHex(16): 16 random bytes => 32 hex characters.
		t.Fatalf("setup token %q must be mr-setup- + 32 hex chars", tok)
	}

	// (a) No token at all.
	rec := a.postFrom(t, "203.0.113.9:4444", "/admin/setup/complete",
		setupRequest{Username: "attacker", Password: "supersecret1"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no token: got %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "setup_token_required" {
		t.Fatalf("error code = %q, want setup_token_required", code)
	}
	// The response must not leak the real token.
	if strings.Contains(rec.Body.String(), a.srv.currentSetupToken()) {
		t.Fatal("403 response leaked the setup token")
	}
	if n, err := a.db.UserCount(); err != nil || n != 0 {
		t.Fatalf("user count = %d (err %v), want 0", n, err)
	}

	// A wrong token is refused just the same.
	rec = a.postFrom(t, "203.0.113.10:4444", "/admin/setup/complete",
		setupRequest{Username: "attacker", Password: "supersecret1", SetupToken: "mr-setup-0000000000000000"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong token: got %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if n, err := a.db.UserCount(); err != nil || n != 0 {
		t.Fatalf("user count = %d (err %v), want 0", n, err)
	}

	// (b) Correct token succeeds.
	token := a.srv.currentSetupToken()
	rec = a.postFrom(t, "203.0.113.11:4444", "/admin/setup/complete",
		setupRequest{Username: "root", Password: "supersecret1", SetupToken: token})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if n, err := a.db.UserCount(); err != nil || n != 1 {
		t.Fatalf("user count = %d (err %v), want 1", n, err)
	}

	// (c) The token is voided, so a second setup is a plain 409.
	rec = a.postFrom(t, "203.0.113.12:4444", "/admin/setup/complete",
		setupRequest{Username: "other", Password: "supersecret2", SetupToken: token})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second setup: got %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "setup_already_complete" {
		t.Fatalf("error code = %q, want setup_already_complete", code)
	}
	if n, err := a.db.UserCount(); err != nil || n != 1 {
		t.Fatalf("user count = %d (err %v), want 1", n, err)
	}
	if got := a.srv.currentSetupToken(); got != "" {
		t.Fatalf("setup token must be voided after setup, got %q", got)
	}
}

// TestSetupFromLoopbackSkipsToken keeps the local first-run flow friction-free:
// a request from 127.0.0.1 needs no token.
func TestSetupFromLoopbackSkipsToken(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:5555", "[::1]:5555"} {
		a := newAuthHarness(t, false)
		rec := a.postFrom(t, addr, "/admin/setup/complete",
			setupRequest{Username: "root", Password: "supersecret1"})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200: %s", addr, rec.Code, rec.Body.String())
		}
		if n, err := a.db.UserCount(); err != nil || n != 1 {
			t.Fatalf("%s: user count = %d (err %v), want 1", addr, n, err)
		}
	}
}

// TestCreateFirstUserAtomic exercises the store-level guard directly: a second
// concurrent-style insert must report false and leave exactly one row.
func TestCreateFirstUserAtomic(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "first.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	first := &model.User{ID: store.NewID("usr"), Username: "a", PasswordHash: "h", CreatedAt: store.Now()}
	created, err := db.CreateFirstUser(first)
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}

	second := &model.User{ID: store.NewID("usr"), Username: "b", PasswordHash: "h", CreatedAt: store.Now()}
	created, err = db.CreateFirstUser(second)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if created {
		t.Fatal("second insert must not create a user")
	}
	if n, err := db.UserCount(); err != nil || n != 1 {
		t.Fatalf("user count = %d (err %v), want 1", n, err)
	}
}

// sessionCookieValue pulls one cookie value out of a recorder's Set-Cookie
// headers (empty when the response did not set it).
func sessionCookieValue(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// authedPost issues a request with an explicit session cookie and CSRF header so
// a test can act as one specific session.
func (a *authHarness) authedPost(t *testing.T, path, sessionValue, csrf string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.RemoteAddr = "203.0.113.7:54321"
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionValue})
	if csrf != "" {
		req.Header.Set(csrfHeader, csrf)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// TestChangePasswordRevokesOtherSessions covers SEC-05: a password change must
// invalidate every other session AND rotate the session that performed it, so a
// stolen cookie cannot outlive the change.
func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	a := newAuthHarness(t, true)
	user, err := a.db.UserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}

	const (
		activeValue = "active-session-cookie-value"
		activeCSRF  = "active-csrf-token"
		stolenValue = "stolen-session-cookie-value"
		stolenCSRF  = "stolen-csrf-token"
		newPassword = "brand-new-password"
	)
	now := store.Now()
	for _, s := range []*model.Session{
		{ID: store.HashToken(activeValue), UserID: user.ID, CSRFToken: activeCSRF, CreatedAt: now, ExpiresAt: now + 3600},
		{ID: store.HashToken(stolenValue), UserID: user.ID, CSRFToken: stolenCSRF, CreatedAt: now, ExpiresAt: now + 3600},
	} {
		if err := a.db.CreateSession(s); err != nil {
			t.Fatal(err)
		}
	}

	rec := a.authedPost(t, "/admin/auth/password", activeValue, activeCSRF,
		passwordRequest{Current: adminPassword, New: newPassword})
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Response contract: a fresh CSRF token for the rotated session, no
	// reauth_required on the success path.
	var body struct {
		OK             bool   `json:"ok"`
		CSRFToken      string `json:"csrf_token"`
		ReauthRequired bool   `json:"reauth_required"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if !body.OK || body.ReauthRequired {
		t.Fatalf("body = %+v, want ok=true and no reauth_required", body)
	}
	if body.CSRFToken == "" || body.CSRFToken == activeCSRF {
		t.Fatalf("csrf_token = %q, must be new and non-empty", body.CSRFToken)
	}

	// The old session id (the one used for the request) is gone server-side.
	if _, err := a.db.SessionByID(store.HashToken(activeValue), store.Now()); err == nil {
		t.Fatal("the session that changed the password must be rotated away")
	}
	// The other session is revoked: a stolen cookie is dead.
	if _, err := a.db.SessionByID(store.HashToken(stolenValue), store.Now()); err == nil {
		t.Fatal("other sessions must be revoked after a password change")
	}

	// The replacement cookies work and carry the returned CSRF token.
	rotatedValue := sessionCookieValue(t, rec, sessionCookie)
	if rotatedValue == "" || rotatedValue == activeValue {
		t.Fatalf("session cookie = %q, want a new value", rotatedValue)
	}
	if got := sessionCookieValue(t, rec, csrfCookie); got != body.CSRFToken {
		t.Fatalf("csrf cookie = %q, want %q", got, body.CSRFToken)
	}
	if _, err := a.db.SessionByID(store.HashToken(rotatedValue), store.Now()); err != nil {
		t.Fatalf("rotated session not stored: %v", err)
	}

	// A write with the rotated cookie but the pre-rotation CSRF header is
	// rejected, proving the token really changed.
	req := httptest.NewRequest(http.MethodPost, "/admin/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: rotatedValue})
	req.Header.Set(csrfHeader, activeCSRF)
	oldCSRF := httptest.NewRecorder()
	a.h.ServeHTTP(oldCSRF, req)
	if oldCSRF.Code != http.StatusForbidden {
		t.Fatalf("stale CSRF token: got %d, want 403", oldCSRF.Code)
	}

	// The new password is the one that now authenticates.
	if rec := a.login(t, "admin", adminPassword); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password login: got %d, want 401", rec.Code)
	}
	if rec := a.login(t, "admin", newPassword); rec.Code != http.StatusOK {
		t.Fatalf("new password login: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
