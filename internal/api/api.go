// Package api wires the admin HTTP API (session auth + CSRF) onto the domain
// services. Handlers stay thin: validation and response shaping only.
package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/myrouter/myrouter/internal/gateway"
	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

// Server is the admin API server.
type Server struct {
	db           *store.DB
	box          *secret.Box
	registry     *provider.Registry
	guard        *guard.Engine
	gateway      *gateway.Gateway
	identity     *guard.Identity
	log          *logx.Logger
	version      string
	allowPrivate bool
	cfg          ServerConfig

	// In-memory failed-attempt counters for the sensitive endpoints. They live
	// for the process lifetime and are intentionally not persisted.
	ipLimiter        *attemptLimiter
	userLimiter      *attemptLimiter
	sensitiveLimiter *attemptLimiter

	// setupToken guards first-run bootstrap against remote takeover. It is
	// generated when the server starts without any admin, printed once to the
	// log (so the operator can copy it) and voided after a successful setup.
	// It is never exposed through the API.
	setupMu    sync.Mutex
	setupToken string

	// playgroundSem caps concurrent Test Lab calls so a runaway loop cannot
	// burn the upstream provider quota.
	playgroundSem chan struct{}
}

// ServerConfig holds runtime knobs used by the admin API.
type ServerConfig struct {
	Version      string
	AllowPrivate bool
	DevMode      bool
}

// Deps are the shared services the API depends on.
type Deps struct {
	DB       *store.DB
	Box      *secret.Box
	Registry *provider.Registry
	Guard    *guard.Engine
	Gateway  *gateway.Gateway
	Identity *guard.Identity
	Log      *logx.Logger
	Config   ServerConfig
}

// New builds the admin API server.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = logx.Default()
	}
	s := &Server{
		db: d.DB, box: d.Box, registry: d.Registry, guard: d.Guard, gateway: d.Gateway,
		identity: d.Identity, log: d.Log, version: d.Config.Version,
		allowPrivate: d.Config.AllowPrivate, cfg: d.Config,
		ipLimiter:        newAttemptLimiter(maxFailuresPerIP, attemptWindow),
		userLimiter:      newAttemptLimiter(maxFailuresPerUser, attemptWindow),
		sensitiveLimiter: newAttemptLimiter(maxFailuresPerIP, attemptWindow),
		playgroundSem:    make(chan struct{}, 2),
	}
	s.initSetupToken()
	return s
}

// initSetupToken mints the first-run setup token when no admin exists yet. The
// token is logged deliberately: it is the only way an operator can finish setup
// from a non-loopback address, and anyone able to read the log already owns the
// process.
func (s *Server) initSetupToken() {
	if s.db == nil {
		return
	}
	n, err := s.db.UserCount()
	if err != nil || n > 0 {
		return
	}
	token := "mr-setup-" + randHex(16) // 16 random bytes => 32 hex characters
	s.setupMu.Lock()
	s.setupToken = token
	s.setupMu.Unlock()
	s.log.Warn("first-run setup required",
		logx.F("setup_token", token),
		logx.F("hint", "required for non-loopback requests to POST /admin/setup/complete"),
	)
}

// currentSetupToken returns the active setup token (empty once setup is done).
func (s *Server) currentSetupToken() string {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupToken
}

// clearSetupToken voids the token after a successful bootstrap.
func (s *Server) clearSetupToken() {
	s.setupMu.Lock()
	s.setupToken = ""
	s.setupMu.Unlock()
}

// Handler mounts every admin route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public auth endpoints.
	mux.HandleFunc("POST /admin/auth/login", s.handleLogin)
	mux.HandleFunc("POST /admin/auth/logout", s.withAuth(s.handleLogout))
	mux.HandleFunc("GET /admin/auth/me", s.withAuth(s.handleMe))
	mux.HandleFunc("POST /admin/auth/password", s.withAuth(s.handleChangePassword))

	// Bootstrap / setup wizard.
	mux.HandleFunc("GET /admin/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /admin/setup/complete", s.handleSetupComplete)

	// Providers.
	mux.HandleFunc("GET /admin/providers", s.withAuth(s.handleListProviders))
	mux.HandleFunc("POST /admin/providers", s.withAuth(s.handleCreateProvider))
	// Draft connection test: validates type/base_url/api_key without persisting
	// anything. Must be registered before the {id} patterns.
	mux.HandleFunc("POST /admin/providers/test", s.withAuth(s.handleTestProviderDraft))
	mux.HandleFunc("GET /admin/providers/{id}", s.withAuth(s.handleGetProvider))
	mux.HandleFunc("PATCH /admin/providers/{id}", s.withAuth(s.handleUpdateProvider))
	mux.HandleFunc("DELETE /admin/providers/{id}", s.withAuth(s.handleDeleteProvider))
	mux.HandleFunc("POST /admin/providers/{id}/test", s.withAuth(s.handleTestProvider))
	mux.HandleFunc("POST /admin/providers/{id}/refresh-models", s.withAuth(s.handleRefreshModels))
	mux.HandleFunc("GET /admin/providers/{id}/models", s.withAuth(s.handleProviderModels))

	// Models.
	mux.HandleFunc("GET /admin/models", s.withAuth(s.handleListModels))

	// Routes.
	mux.HandleFunc("GET /admin/routes", s.withAuth(s.handleListRoutes))
	mux.HandleFunc("POST /admin/routes", s.withAuth(s.handleCreateRoute))
	mux.HandleFunc("POST /admin/routes/auto", s.withAuth(s.handleAutoRoutes))
	mux.HandleFunc("PATCH /admin/routes/{id}", s.withAuth(s.handleUpdateRoute))
	mux.HandleFunc("DELETE /admin/routes/{id}", s.withAuth(s.handleDeleteRoute))

	// API keys.
	mux.HandleFunc("GET /admin/api-keys", s.withAuth(s.handleListAPIKeys))
	mux.HandleFunc("POST /admin/api-keys", s.withAuth(s.handleCreateAPIKey))
	mux.HandleFunc("PATCH /admin/api-keys/{id}", s.withAuth(s.handleUpdateAPIKey))
	mux.HandleFunc("POST /admin/api-keys/{id}/revoke", s.withAuth(s.handleRevokeAPIKey))

	// Guard: policies.
	mux.HandleFunc("GET /admin/policies", s.withAuth(s.handleListPolicies))
	mux.HandleFunc("POST /admin/policies", s.withAuth(s.handleCreatePolicy))
	mux.HandleFunc("PATCH /admin/policies/{id}", s.withAuth(s.handleUpdatePolicy))
	mux.HandleFunc("DELETE /admin/policies/{id}", s.withAuth(s.handleDeletePolicy))

	// Guard: IP rules.
	mux.HandleFunc("GET /admin/ip-rules", s.withAuth(s.handleListIPRules))
	mux.HandleFunc("POST /admin/ip-rules", s.withAuth(s.handleCreateIPRule))
	mux.HandleFunc("DELETE /admin/ip-rules/{id}", s.withAuth(s.handleDeleteIPRule))

	// Guard: devices.
	mux.HandleFunc("GET /admin/devices", s.withAuth(s.handleListDevices))
	mux.HandleFunc("POST /admin/devices/{id}/block", s.withAuth(s.handleBlockDevice))
	mux.HandleFunc("POST /admin/devices/{id}/rename", s.withAuth(s.handleRenameDevice))

	// Guard: blocked.
	mux.HandleFunc("GET /admin/blocked", s.withAuth(s.handleListBlocked))
	mux.HandleFunc("DELETE /admin/blocked/{id}", s.withAuth(s.handleUnblock))

	// Activity & usage.
	mux.HandleFunc("GET /admin/activity", s.withAuth(s.handleListActivity))
	mux.HandleFunc("GET /admin/activity/{id}", s.withAuth(s.handleGetActivity))
	mux.HandleFunc("GET /admin/usage", s.withAuth(s.handleUsage))

	// Dashboard & settings.
	mux.HandleFunc("GET /admin/overview", s.withAuth(s.handleOverview))
	mux.HandleFunc("GET /admin/overview/series", s.withAuth(s.handleSeries))
	mux.HandleFunc("GET /admin/live", s.withAuth(s.handleLive))
	// Test Lab: admin-only calls through the real routing pipeline.
	mux.HandleFunc("POST /admin/playground/chat", s.withAuth(s.handlePlaygroundChat))
	mux.HandleFunc("POST /admin/playground/stream", s.withAuth(s.handlePlaygroundStream))
	mux.HandleFunc("GET /admin/settings", s.withAuth(s.handleGetSettings))
	mux.HandleFunc("PATCH /admin/settings", s.withAuth(s.handleUpdateSettings))
	// Health. The detailed admin view (provider/degraded counters, trusted
	// proxy count) is internal state and must not be readable without a
	// session; unauthenticated liveness checks use the public /healthz route
	// mounted by cmd/myrouter, which only reports {"status","version"}.
	mux.HandleFunc("GET /admin/health", s.withAuth(s.handleHealth))

	s.RegisterConfigRoutes(mux)

	return drainRequestBody(mux)
}

// drainRequestBody consumes any bytes the handler did not read before the
// connection is closed. Handlers that answer without a body (health probes,
// "test connection", revoke, CSRF rejections) would otherwise leave the
// request body queued; when the client asks for Connection: close, the kernel
// answers the close with a TCP RST and the client loses the response
// (observed as WinError 10054 on Windows loopback, and the same failure mode
// exists for any non-browser client using Connection: close).
func drainRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type handlerFunc func(w http.ResponseWriter, r *http.Request, sess *authSession)

func (s *Server) withAuth(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.currentSession(r)
		if sess == nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !s.checkCSRF(r, sess) {
				writeErr(w, http.StatusForbidden, "csrf_failed", "CSRF token missing or invalid")
				return
			}
		}
		next(w, r, sess)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOK(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// decode reads a JSON body with a hard size limit.
func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *Server) pathID(r *http.Request) string { return r.PathValue("id") }

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return strings.ToUpper(jsonHex(b))
}

func jsonHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0xf]
	}
	return string(out)
}

// randHex returns n random bytes from crypto/rand, hex-encoded.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return jsonHex(b)
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
