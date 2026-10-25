package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// authSession is the authenticated admin context.
type authSession struct {
	Session *model.Session
	User    *model.User
}

const (
	sessionCookie = "mr_session"
	csrfCookie    = "mr_csrf"
	csrfHeader    = "X-CSRF-Token"
	sessionTTL    = 7 * 24 * time.Hour
)

// ---------------------------------------------------------------------------
// Attempt limiting (in-memory, per-IP and per-username)
// ---------------------------------------------------------------------------

const (
	// maxFailuresPerIP is the failed-attempt budget for one client IP.
	maxFailuresPerIP = 10
	// maxFailuresPerUser is the failed-attempt budget for one username.
	maxFailuresPerUser = 5
	// attemptWindow is the sliding window the failure counters live in.
	attemptWindow = 15 * time.Minute
)

// attemptLimiter is a small in-memory sliding-window failure counter keyed by an
// opaque string (client IP or username). Nothing is persisted: a restart clears
// every lock. Safe for concurrent use.
type attemptLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	now      func() time.Time
	failures map[string][]time.Time
}

func newAttemptLimiter(limit int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{
		limit: limit, window: window, now: time.Now,
		failures: make(map[string][]time.Time),
	}
}

// prune drops timestamps outside the window and returns how many remain.
// The caller must hold l.mu.
func (l *attemptLimiter) prune(key string, now time.Time) int {
	cutoff := now.Add(-l.window)
	list := l.failures[key]
	keep := list[:0]
	for _, ts := range list {
		if ts.After(cutoff) {
			keep = append(keep, ts)
		}
	}
	if len(keep) == 0 {
		delete(l.failures, key)
		return 0
	}
	l.failures[key] = keep
	return len(keep)
}

// blocked reports whether key has exhausted its budget, plus how long until the
// oldest failure leaves the window (used for Retry-After).
func (l *attemptLimiter) blocked(key string) (bool, time.Duration) {
	if key == "" {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.prune(key, now) < l.limit {
		return false, 0
	}
	retry := l.window - now.Sub(l.failures[key][0])
	if retry <= 0 {
		retry = time.Second
	}
	return true, retry
}

// fail records one failed attempt for key.
func (l *attemptLimiter) fail(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(key, now)
	l.failures[key] = append(l.failures[key], now)
}

// reset clears the history for key (called after a successful login).
func (l *attemptLimiter) reset(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// setClock overrides the time source of every limiter. Tests use it to advance
// the lockout window instead of sleeping.
func (s *Server) setClock(now func() time.Time) {
	s.ipLimiter.now = now
	s.userLimiter.now = now
	s.sensitiveLimiter.now = now
}

// clientIP resolves the client address used as the rate-limit key. It reuses the
// trusted-proxy aware Identity when configured and otherwise falls back to the
// raw peer address; X-Forwarded-For is never trusted on its own.
func (s *Server) clientIP(r *http.Request) string {
	if s.identity != nil {
		if ip := s.identity.ClientIP(r); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// tooManyAttempts writes the 429 response when the per-IP or per-username
// budget is exhausted and reports whether the caller must stop.
func (s *Server) tooManyAttempts(w http.ResponseWriter, ipKey, userKey string) bool {
	blocked := false
	var retry time.Duration
	if ok, d := s.ipLimiter.blocked(ipKey); ok {
		blocked, retry = true, d
	}
	if ok, d := s.userLimiter.blocked(userKey); ok && (!blocked || d > retry) {
		blocked, retry = true, d
	}
	if !blocked {
		return false
	}
	writeTooManyAttempts(w, retry)
	return true
}

// sensitiveBlocked applies the per-IP limiter to the other sensitive endpoints
// (setup, password change) and reports whether the caller must stop.
func (s *Server) sensitiveBlocked(w http.ResponseWriter, ipKey string) bool {
	ok, retry := s.sensitiveLimiter.blocked(ipKey)
	if !ok {
		return false
	}
	writeTooManyAttempts(w, retry)
	return true
}

// writeTooManyAttempts emits the shared 429 body plus the Retry-After hint.
func writeTooManyAttempts(w http.ResponseWriter, retry time.Duration) {
	secs := int(retry.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, "too_many_attempts",
		"too many failed attempts; try again later")
}

// currentSession resolves the session cookie to a user.
func (s *Server) currentSession(r *http.Request) *authSession {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	sess, err := s.db.SessionByID(store.HashToken(c.Value), store.Now())
	if err != nil {
		return nil
	}
	user, err := s.db.UserByID(sess.UserID)
	if err != nil {
		return nil
	}
	return &authSession{Session: sess, User: user}
}

// checkCSRF validates the double-submit token: the X-CSRF-Token header must
// match the session's server-side token. The cookie alone is NOT accepted —
// an attacker's browser would send it automatically.
func (s *Server) checkCSRF(r *http.Request, sess *authSession) bool {
	token := r.Header.Get(csrfHeader)
	return token != "" && store.ConstantTimeEqual(token, sess.Session.CSRFToken)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin authenticates the admin and sets a session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "invalid_credentials", "username and password are required")
		return
	}
	ipKey := s.clientIP(r)
	userKey := strings.ToLower(req.Username)
	if s.tooManyAttempts(w, ipKey, userKey) {
		return
	}
	user, err := s.db.UserByUsername(req.Username)
	if err != nil {
		// Constant-ish time: still run a bcrypt compare against a dummy hash.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$abcdefghijklmnopqrstuv"), []byte(req.Password))
		s.ipLimiter.fail(ipKey)
		s.userLimiter.fail(userKey)
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		s.ipLimiter.fail(ipKey)
		s.userLimiter.fail(userKey)
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	// Successful login clears both counters for this IP and username.
	s.ipLimiter.reset(ipKey)
	s.userLimiter.reset(userKey)

	sessionValue := store.RandomBase64(32)
	csrf := store.RandomBase64(32)
	now := time.Now()
	sess := &model.Session{
		ID: store.HashToken(sessionValue), UserID: user.ID, CSRFToken: csrf,
		CreatedAt: now.Unix(), ExpiresAt: now.Add(sessionTTL).Unix(),
	}
	if err := s.db.CreateSession(sess); err != nil {
		s.log.Error("session create failed", logx.F("user", user.ID), logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "session_failed", "could not create session")
		return
	}
	setSessionCookies(w, r, sessionValue, csrf, now)
	writeOK(w, map[string]any{"username": user.Username, "csrf_token": csrf})
}

// secureCookies reports whether the session cookies may be marked Secure. It is
// on for HTTPS (direct or behind a TLS-terminating proxy); plain-http localhost
// dev still works because browsers permit Secure cookies on http://localhost.
func secureCookies(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") || !isLoopbackRequest(r)
}

// setSessionCookies writes the session + CSRF cookie pair with the hardened
// attributes used everywhere (HttpOnly on the session, SameSite=Lax, Secure on
// anything that is not loopback). Centralised so login and session rotation can
// never drift apart.
func setSessionCookies(w http.ResponseWriter, r *http.Request, sessionValue, csrf string, now time.Time) {
	secure := secureCookies(r)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sessionValue, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: now.Add(sessionTTL), Secure: secure,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: csrf, Path: "/", HttpOnly: false, SameSite: http.SameSiteLaxMode,
		Expires: now.Add(sessionTTL), Secure: secure,
	})
}

// clearSessionCookies expires both auth cookies on the client.
func clearSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteLaxMode,
	})
}

// handleLogout destroys the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess *authSession) {
	_ = s.db.DeleteSession(sess.Session.ID)
	clearSessionCookies(w)
	writeOK(w, map[string]bool{"ok": true})
}

// handleMe returns the current admin.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, sess *authSession) {
	writeOK(w, map[string]any{
		"username":   sess.User.Username,
		"csrf_token": sess.Session.CSRFToken,
		"version":    s.version,
	})
}

type passwordRequest struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

// isLoopbackRequest reports whether the request came from the local machine,
// where Secure cookies are not required (and work on http://localhost).
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackIP reports whether an already-resolved client IP (as returned by
// clientIP, which honours the trusted-proxy configuration) is the local host.
func isLoopbackIP(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

// handleChangePassword updates the admin password.
//
// SEC-05: a password change must revoke every other session and rotate the
// session that performed the change. A stolen cookie therefore dies even if the
// attacker already holds a valid session id. The revocation result is treated as
// a hard failure: the handler never reports success while old sessions may still
// be live.
//
// Response contract:
//   - 200 {"ok":true,"csrf_token":"..."} on full success. Both cookies are
//     replaced, so the client MUST swap in the new CSRF token (the old session id
//     and token are gone). "reauth_required" is absent/false.
//   - 500 {"error":{"code":"session_revoke_failed"|"session_failed", ...},
//     "reauth_required":true}: the password is already updated, but the caller
//     must sign in again because no trustworthy session could be established.
//     Cookies are cleared in that case.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, sess *authSession) {
	ipKey := s.clientIP(r)
	if s.sensitiveBlocked(w, ipKey) {
		return
	}
	var req passwordRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if len(req.New) < 8 {
		writeErr(w, http.StatusBadRequest, "weak_password", "new password must be at least 8 characters")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(sess.User.PasswordHash), []byte(req.Current)) != nil {
		s.sensitiveLimiter.fail(ipKey)
		writeErr(w, http.StatusForbidden, "invalid_credentials", "current password is incorrect")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		s.log.Error("password hash failed", logx.F("user", sess.User.ID), logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "hash_failed", "could not hash password")
		return
	}
	if _, err := s.db.Exec(`UPDATE user SET password_hash=? WHERE id=?`, string(hash), sess.User.ID); err != nil {
		s.log.Error("password update failed", logx.F("user", sess.User.ID), logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "update_failed", "could not update password")
		return
	}

	// From here on the password is already changed. Any failure must force a
	// fresh login instead of leaving an unverifiable session behind.
	sessionValue := store.RandomBase64(32)
	csrf := store.RandomBase64(32)
	now := time.Now()
	rotated := &model.Session{
		ID: store.HashToken(sessionValue), UserID: sess.User.ID, CSRFToken: csrf,
		CreatedAt: now.Unix(), ExpiresAt: now.Add(sessionTTL).Unix(),
	}
	if err := s.db.CreateSession(rotated); err != nil {
		s.log.Error("session rotation failed", logx.F("user", sess.User.ID), logx.F("error", err.Error()))
		// Best effort: drop everything for this user so no pre-change cookie
		// survives, then tell the caller to sign in again.
		if _, revErr := s.db.RevokeOtherSessions(sess.User.ID, ""); revErr != nil {
			s.log.Warn("session revocation after rotation failure also failed",
				logx.F("user", sess.User.ID), logx.F("error", revErr.Error()))
		}
		clearSessionCookies(w)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"code":    "session_failed",
				"message": "password changed but the session could not be rotated; sign in again with the new password",
			},
			"reauth_required": true,
		})
		return
	}

	// Revoke every session except the freshly minted one. That includes the
	// session id used for this request, so the old cookie is dead on the wire.
	revoked, err := s.db.RevokeOtherSessions(sess.User.ID, rotated.ID)
	if err != nil {
		s.log.Error("session revocation failed", logx.F("user", sess.User.ID), logx.F("error", err.Error()))
		// Do not leave the half-created rotation behind and do not pretend the
		// request succeeded: the caller must re-authenticate.
		if delErr := s.db.DeleteSession(rotated.ID); delErr != nil {
			s.log.Warn("rotated session cleanup failed",
				logx.F("user", sess.User.ID), logx.F("error", delErr.Error()))
		}
		clearSessionCookies(w)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"code":    "session_revoke_failed",
				"message": "password changed but other sessions could not be revoked; sign in again with the new password",
			},
			"reauth_required": true,
		})
		return
	}
	s.log.Info("password changed", logx.F("user", sess.User.ID), logx.F("revoked_sessions", revoked))

	// The old session id/token are now invalid server-side; hand the client the
	// replacement pair so the next request carries a fresh CSRF token.
	setSessionCookies(w, r, sessionValue, csrf, now)
	writeOK(w, map[string]any{"ok": true, "csrf_token": csrf})
}

// ---------------------------------------------------------------------------
// Setup wizard
// ---------------------------------------------------------------------------

// handleSetupStatus reports whether first-run setup is required.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.db.UserCount()
	if err != nil {
		s.log.Error("setup status query failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "db_error", "could not read setup status")
		return
	}
	done, _ := s.db.GetSetting("setup_complete")
	writeOK(w, map[string]any{
		"needs_setup": n == 0 && done != "1",
		"has_admin":   n > 0,
		"version":     s.version,
	})
}

type setupRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	Port         int    `json:"port"`
	MasterKey    string `json:"master_key"`
	SkipProvider bool   `json:"skip_provider"`
	// SetupToken is the first-run token printed to the server log. It is only
	// required for requests that do not originate from the local machine.
	SetupToken string `json:"setup_token"`
}

// handleSetupComplete creates the first admin account.
//
// SEC-01: the endpoint is unauthenticated by necessity (there is no admin yet),
// so a remote caller must present the first-run setup token that was logged at
// startup. Loopback callers are exempt: they already have local access to the
// process and its log.
func (s *Server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	ipKey := s.clientIP(r)
	if s.sensitiveBlocked(w, ipKey) {
		return
	}
	n, err := s.db.UserCount()
	if err != nil {
		s.log.Error("setup user count failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "db_error", "could not read setup status")
		return
	}
	if n > 0 {
		writeErr(w, http.StatusConflict, "setup_already_complete", "an admin account already exists")
		return
	}
	var req setupRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	// Remote callers must prove they can read the server log. The comparison is
	// constant time and the response never echoes or hints at the real token.
	if !isLoopbackIP(ipKey) {
		expected := s.currentSetupToken()
		if expected == "" || subtle.ConstantTimeCompare([]byte(req.SetupToken), []byte(expected)) != 1 {
			s.sensitiveLimiter.fail(ipKey)
			writeErr(w, http.StatusForbidden, "setup_token_required",
				"a valid setup_token is required; it was printed in the server log at startup")
			return
		}
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "invalid_input", "username is required and password must be at least 8 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		s.log.Error("setup password hash failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "hash_failed", "could not hash password")
		return
	}
	user := &model.User{ID: store.NewID("usr"), Username: req.Username, PasswordHash: string(hash), CreatedAt: store.Now()}
	// Atomic first-user insert: the guard above is only a fast path, the SQL
	// statement is what actually makes concurrent setup race-free.
	created, err := s.db.CreateFirstUser(user)
	if err != nil {
		s.log.Error("setup user create failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "create_failed", "could not create admin account")
		return
	}
	if !created {
		s.sensitiveLimiter.fail(ipKey)
		writeErr(w, http.StatusConflict, "setup_already_complete", "an admin account already exists")
		return
	}
	// Bootstrap is done: the token can never be used again.
	s.clearSetupToken()
	_ = s.db.SetSetting("setup_complete", "1")
	if req.Port > 0 {
		_ = s.db.SetSetting("api_port", itoa(req.Port))
	}
	writeOK(w, map[string]bool{"ok": true})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
