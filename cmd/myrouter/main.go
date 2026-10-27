// Command myrouter runs the MyRouter self-hosted AI routing gateway:
// a single binary serving the admin SPA and the OpenAI-compatible gateway.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/myrouter/myrouter/internal/api"
	"github.com/myrouter/myrouter/internal/config"
	"github.com/myrouter/myrouter/internal/gateway"
	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/modelmeta"
	"github.com/myrouter/myrouter/internal/modelspec"
	"github.com/myrouter/myrouter/internal/monitor"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/skills"
	"github.com/myrouter/myrouter/internal/store"
	"github.com/myrouter/myrouter/internal/tray"
	"github.com/myrouter/myrouter/web"
)

// version is injected at build time with -ldflags "-X main.version=..."
var version = "0.1.0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	addrFlag := flag.String("addr", "", "listen address (default :20128)")
	// The tray is on by default on Windows desktop builds so a double-clicked
	// .exe never becomes a background process with no way to quit. Use
	// -no-tray (or -tray=false) for servers, services and containers.
	defaultTray := runtime.GOOS == "windows"
	trayFlag := flag.Bool("tray", defaultTray, "show a system-tray icon (default: true on Windows)")
	noTrayFlag := flag.Bool("no-tray", false, "disable the system-tray icon (headless/server mode)")
	openFlag := flag.Bool("open", false, "open the dashboard in the browser after start")
	shortcutFlag := flag.Bool("install-shortcut", false, "create a Desktop shortcut that starts MyRouter in tray mode")
	autostartFlag := flag.Bool("autostart", false, "also launch MyRouter at login (implies -install-shortcut)")
	installOnlyFlag := flag.Bool("install-only", false, "install the desktop shortcut and exit")
	// Opt-in clean start: move the live database aside and boot as a fresh
	// install. Off by default on purpose - waking up to a wiped gateway would
	// be a catastrophic surprise, so this must always be an explicit request.
	resetDataFlag := flag.Bool("reset-data", false, "back up and clear the local database, then start from first-run setup")
	flag.Parse()

	showTray := *trayFlag && !*noTrayFlag

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := logx.Default()
	cfg := config.Load()
	if *addrFlag != "" {
		cfg.Addr = *addrFlag
	}

	// Services, scheduled tasks and session-0 containers have no desktop; a
	// tray icon cannot exist there, so fall back to headless mode quietly.
	if showTray && !tray.Interactive() {
		log.Info("no interactive desktop detected; system tray disabled")
		showTray = false
	}

	dashboardURL := dashboardAddr(cfg.Addr)
	// Double-clicking the desktop icon while MyRouter is already running should
	// focus the dashboard instead of failing to bind the port.
	if (showTray || *openFlag) && alreadyRunning(cfg.Addr) {
		if err := tray.OpenBrowser(dashboardURL); err != nil {
			log.Warn("could not open dashboard", logx.F("error", err.Error()))
		}
		return
	}

	if *shortcutFlag || *autostartFlag || *installOnlyFlag {
		exe, err := os.Executable()
		if err != nil {
			log.Warn("cannot resolve executable for shortcut", logx.F("error", err.Error()))
		} else if link, err := tray.InstallShortcut(exe, *autostartFlag); err != nil {
			log.Warn("shortcut install failed", logx.F("error", err.Error()))
		} else {
			log.Info("desktop shortcut installed", logx.F("path", link), logx.F("autostart", *autostartFlag))
		}
		if *installOnlyFlag {
			return
		}
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Error("cannot create data dir", logx.F("error", err.Error()))
		os.Exit(1)
	}
	if *resetDataFlag || envBool("MYROUTER_RESET_DATA") {
		moved, err := store.ResetData(cfg.DataDir)
		if err != nil {
			log.Error("reset-data failed", logx.F("error", err.Error()))
			os.Exit(1)
		}
		log.Warn("reset-data: local database cleared, starting fresh", logx.F("backups", strings.Join(moved, ",")))
		for _, m := range moved {
			fmt.Println("backed up:", m)
		}
	}
	// The desktop build is linked with -H windowsgui (no console), so mirror
	// logs to a file the operator can inspect when the tray misbehaves. A
	// console build launched by double-click also hides its console, so the
	// file is the only place to see startup problems there too.
	if showTray {
		if f, err := os.OpenFile(filepath.Join(cfg.DataDir, "myrouter.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			logx.SetOutput(f)
			defer f.Close()
			log.Info("logging to file", logx.F("path", f.Name()))
		}
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "myrouter.db"))
	if err != nil {
		log.Error("database open failed", logx.F("error", err.Error()))
		os.Exit(1)
	}
	defer db.Close()
	// A fresh process means a fresh login. Sessions persisted in SQLite would
	// otherwise let a browser tab (or a stale cookie) re-enter the dashboard
	// after the app was quit and reopened, which is not what "quit" implies.
	if n, err := db.DeleteAllSessions(); err != nil {
		log.Warn("could not clear stale admin sessions", logx.F("error", err.Error()))
	} else if n > 0 {
		log.Info("cleared admin sessions from the previous run", logx.F("sessions", n))
	}
	// One-time browser cache eviction after an upgrade (see uicache.go).
	initUICache(cfg.DataDir, version)

	box, err := loadBox(cfg, db)
	if err != nil {
		log.Error("master key initialisation failed", logx.F("error", err.Error()))
		os.Exit(1)
	}

	// Bootstrap defaults on first run.
	if err := seedDefaults(db); err != nil {
		log.Warn("seed defaults failed", logx.F("error", err.Error()))
	}
	// Built-in skills are seeded once and bound globally, so every model alias
	// automatically inherits them (including routes created later).
	if err := db.SeedBuiltinSkills(); err != nil {
		log.Warn("seed skills failed", logx.F("error", err.Error()))
	}
	gatewayURL := gatewayBaseURL(db, dashboardURL)

	allowPrivate := settingBool(db, "allow_private_upstreams", cfg.DevMode)
	trusted := guard.TrustedProxiesFromSettings(db)
	registry := provider.NewRegistry(provider.GuardedClient(allowPrivate, 0))
	identity := guard.NewIdentity(trusted)
	// Official model metadata (context window, prices per 1M tokens, vision /
	// tools / reasoning capabilities) from the models.dev catalog. Loading the
	// cached/embedded snapshot is instant; the network refresh runs in the
	// background and never blocks startup.
	modelCatalog := modelspec.New(cfg.DataDir)
	if err := modelCatalog.Load(); err != nil {
		log.Warn("model catalog cache load failed", logx.F("error", err.Error()))
	}

	engine := guard.New(db, guard.ZLogger{L: log})
	gw := gateway.New(gateway.Options{
		DB: db, Registry: registry, Box: box, Guard: engine, Identity: identity,
		Client: provider.GuardedClient(allowPrivate, 0), Log: log, Version: version,
	})
	admin := api.New(api.Deps{
		DB: db, Box: box, Registry: registry, Guard: engine, Gateway: gw, Identity: identity, Log: log,
		Config: api.ServerConfig{
			Version: version, AllowPrivate: allowPrivate, DevMode: cfg.DevMode,
			Addr: cfg.Addr, DataDir: cfg.DataDir,
		},
		Catalog: modelCatalog,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Start(ctx)
	monitor.New(db, registry, box, 5*time.Minute, log).Start(ctx)
	go runScheduler(ctx, db, engine, cfg, log)
	// Refresh retrieval skills (local code tree, ai-memory wiki) in the
	// background so their indexes stay current without manual syncs.
	go skills.NewSyncer(db, log).Run(ctx)
	// Keep the model metadata catalog fresh and backfill models that were
	// discovered before the catalog was available.
	go modelCatalog.Start(ctx)
	go modelmeta.BackfillLoop(ctx, db, modelCatalog, log)

	mux := http.NewServeMux()
	mux.Handle("/admin/", admin.Handler())
	mux.Handle("/v1/", gw.Handler())
	// Public liveness probe: minimal on purpose. The detailed health view
	// (provider counters, degraded count, trusted proxies) lives behind the
	// admin session at GET /admin/health.
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + version + `"}`))
	}))
	mux.Handle("/", spaHandler(log))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           securityHeaders(corsMiddleware(db, mux)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Info("myrouter listening", logx.F("addr", cfg.Addr), logx.F("version", version),
			logx.F("data", cfg.DataDir), logx.F("spa", web.Available()))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", logx.F("error", err.Error()))
			os.Exit(1)
		}
	}()

	if *openFlag {
		if err := tray.OpenBrowser(dashboardURL); err != nil {
			log.Warn("could not open dashboard", logx.F("error", err.Error()))
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	quit := make(chan struct{}, 1)
	var trayErr <-chan error
	if showTray {
		errCh := make(chan error, 1)
		trayErr = errCh
		go func() {
			// Run returns nil after a normal Quit (the quit channel already
			// carries that signal), so only real failures are reported here.
			if err := tray.Run(tray.Options{
				Tooltip:      "MyRouter — running",
				DashboardURL: dashboardURL,
				GatewayURL:   gatewayURL,
				OnReady: func() {
					// A console build launched by double-click owns its console;
					// hide it so the app looks like a normal tray app. A console
					// shared with a terminal (CLI run) is left visible.
					if tray.HideOwnConsole() {
						log.Info("tray ready; console window hidden")
					} else {
						log.Info("tray icon ready", logx.F("dashboard", dashboardURL))
					}
				},
				OnQuit: func() {
					select {
					case quit <- struct{}{}:
					default:
					}
				},
			}); err != nil {
				errCh <- err
			}
		}()
	}
	running := true
	for running {
		select {
		case <-stop:
			running = false
		case <-quit:
			running = false
		case err := <-trayErr:
			trayErr = nil
			if errors.Is(err, tray.ErrUnsupported) {
				log.Debug("system tray not supported on this platform")
				continue
			}
			log.Warn("system tray unavailable", logx.F("error", err.Error()))
			// A GUI build has no console, so a failed tray would leave an
			// invisible background process the user can only kill from Task
			// Manager. Tell them and exit instead of hanging around.
			if !tray.HasConsole() {
				tray.ShowError("MyRouter",
					"Ikon system tray gagal dibuat:\n\n"+err.Error()+
						"\n\nMyRouter akan ditutup. Jalankan dengan -no-tray untuk mode headless, "+
						"atau periksa log di data\\myrouter.log.")
				running = false
			}
		}
	}
	// Quitting (tray menu, Ctrl+C, SIGTERM) means the whole system stops:
	// remove the tray icon, cancel every background worker (guard engine,
	// monitor, scheduler) and close the HTTP server. Nothing keeps running
	// in the background after this point.
	tray.Stop()
	cancel()
	log.Info("shutting down")
	engine.Snapshot()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	// Belt and braces: no goroutine may hold the process alive once the user
	// asked to quit, so exit explicitly after the graceful drain.
	os.Exit(0)
}

// dashboardAddr turns a listen address into a browser URL, replacing wildcard
// binds with loopback so the tray can always open the local dashboard.
func dashboardAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:20128"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// alreadyRunning reports whether a MyRouter instance answers on addr.
func alreadyRunning(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 800 * time.Millisecond}
	res, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// gatewayBaseURL prefers the operator's custom base URL (Settings → API) and
// falls back to the local dashboard origin + /v1.
func gatewayBaseURL(db *store.DB, dashboard string) string {
	if v, err := db.GetSetting("base_url"); err == nil {
		if s := strings.TrimSpace(v); s != "" {
			return strings.TrimRight(s, "/")
		}
	}
	return strings.TrimRight(dashboard, "/") + "/v1"
}

// loadBox resolves the master key from env, then a key file in the data dir.
func loadBox(cfg config.Config, db *store.DB) (*secret.Box, error) {
	key := strings.TrimSpace(cfg.MasterKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("MYROUTER_MASTER_KEY"))
	}
	if key == "" {
		keyPath := filepath.Join(cfg.DataDir, "master.key")
		if raw, err := os.ReadFile(keyPath); err == nil {
			key = strings.TrimSpace(string(raw))
		} else if os.IsNotExist(err) {
			// Generate and persist a 32-byte key with owner-only permissions.
			buf := make([]byte, 32)
			if _, err := cryptoRandRead(buf); err != nil {
				return nil, err
			}
			key = base64.StdEncoding.EncodeToString(buf)
			if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
				return nil, fmt.Errorf("write master key: %w", err)
			}
		} else {
			return nil, err
		}
	}
	return secret.New(key)
}

// spaHandler serves the embedded SPA with history-mode fallback.
func spaHandler(log *logx.Logger) http.Handler {
	sub, err := web.FS()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI not built", http.StatusServiceUnavailable)
		})
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(clean, "/")
		if name == "" {
			name = "index.html"
		}
		if f, err := sub.Open(name); err == nil {
			_ = f.Close()
			if ext := path.Ext(name); ext != "" {
				if ct := mime.TypeByExtension(ext); ct != "" {
					w.Header().Set("Content-Type", ct)
				}
				// Windows has no built-in mapping for .webmanifest, which makes
				// browsers reject the PWA manifest as text/plain.
				if ext == ".webmanifest" {
					w.Header().Set("Content-Type", "application/manifest+json")
				}
				// Only fingerprinted build assets may be cached forever. HTML
				// (index.html and history fallbacks) must always revalidate,
				// otherwise a browser keeps loading the previous bundle after
				// an upgrade and the UI silently stays stale.
				if strings.HasPrefix(clean, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					serveShellHeaders(w)
				}
			} else {
				serveShellHeaders(w)
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// History-mode fallback.
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			log.Error("missing index.html")
			http.Error(w, "UI not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveShellHeaders(w)
		_, _ = w.Write(index)
	})
}

// serveShellHeaders marks the SPA shell as uncacheable and, on the first page
// load after an upgrade, asks the browser to drop previously cached responses.
// Hashed /assets/* files keep their immutable cache; only the shell changes.
func serveShellHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	if uiCacheNeedsClear() {
		// "cache" only: cookies and storage must survive so the admin session
		// and the local history are not thrown away by an upgrade.
		w.Header().Set("Clear-Site-Data", `"cache"`)
	}
}

// securityHeaders applies conservative defaults to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com data:; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		// Admin API responses must never be cached.
		if strings.HasPrefix(r.URL.Path, "/admin/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// gatewayRoutes is the set of paths the /v1 router actually serves. CORS
// preflights (OPTIONS) are only answered for these; any other path under /v1/
// is a 404 rather than a 204 so an unregistered URL cannot look healthy.
var gatewayRoutes = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/models":           true,
}

// normalizeOrigin canonicalises an Origin header or allow-list entry so that
// matching is exact but tolerant of case in scheme/host and of a trailing
// slash: the result is "scheme://host[:port]" in lower case.
func normalizeOrigin(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || s == "*" {
		return ""
	}
	s = strings.TrimRight(s, "/")
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.ToLower(s)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// gatewayError writes an OpenAI-style JSON error for the gateway edge.
func gatewayError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "{\"error\":{\"code\":%q,\"message\":%q,\"type\":\"invalid_request_error\"}}\n", code, msg)
}

// corsMiddleware applies the configured CORS policy to the gateway only.
//
// The allow-list is read once, when the middleware is built at start-up: a
// change to cors_origins takes effect after a restart, which keeps the request
// path free of database reads. Requests that carry an Origin header which is
// not on the list are refused with a 403 before reaching the gateway; requests
// without Origin (SDKs, server-to-server calls) are served normally.
// Access-Control-Allow-Credentials is deliberately never sent: the gateway is
// authenticated with a bearer key, not cookies, so browsers must not attach
// ambient credentials.
func corsMiddleware(db *store.DB, next http.Handler) http.Handler {
	allowAll := false
	allowed := make(map[string]bool)
	for _, entry := range store.SplitCSV(settingString(db, "cors_origins", "")) {
		if entry == "*" {
			allowAll = true
			continue
		}
		if norm := normalizeOrigin(entry); norm != "" {
			allowed[norm] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		h := w.Header()
		// The response headers/body depend on Origin, so caches must key on it.
		h.Add("Vary", "Origin")

		// Preflight is only answered for routes the gateway really serves; an
		// unregistered /v1/* path must not look healthy (no blanket 204). This
		// check runs first so an unknown path is a 404 whatever the Origin.
		if r.Method == http.MethodOptions {
			if !gatewayRoutes[r.URL.Path] {
				gatewayError(w, http.StatusNotFound, "not_found", "unknown endpoint")
				return
			}
		}

		// A request without Origin is not a browser cross-origin call (SDK or
		// server-to-server): it is served normally, without any CORS headers.
		origin := r.Header.Get("Origin")
		if origin != "" {
			norm := normalizeOrigin(origin)
			if norm == "" || !(allowAll || allowed[norm]) {
				gatewayError(w, http.StatusForbidden, "origin_not_allowed", "origin not allowed")
				return
			}
			// Echo the request origin (never the literal "*") so the value is an
			// exact match of what the browser sent.
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Device-Id, X-Request-Id")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// runScheduler handles periodic maintenance: log retention + counter snapshots.
func runScheduler(ctx context.Context, db *store.DB, engine *guard.Engine, cfg config.Config, log *logx.Logger) {
	purge := time.NewTicker(1 * time.Hour)
	defer purge.Stop()
	// Run once shortly after boot.
	first := time.NewTimer(30 * time.Second)
	defer first.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			maintenance(db, engine, cfg, log)
		case <-purge.C:
			maintenance(db, engine, cfg, log)
		}
	}
}

func maintenance(db *store.DB, engine *guard.Engine, cfg config.Config, log *logx.Logger) {
	days := cfg.LogRetention
	if v := settingString(db, "log_retention_days", ""); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 {
			days = n
		}
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	deleted, err := db.PurgeRequestLogs(cutoff)
	if err != nil {
		log.Warn("log purge failed", logx.F("error", err.Error()))
	} else if deleted > 0 {
		log.Info("purged request logs", logx.F("rows", deleted), logx.F("retention_days", days))
	}
	_ = db.PurgeExpiredBlocks(store.Now())
	_ = db.PurgeSessions(store.Now())
	engine.Snapshot()
}
