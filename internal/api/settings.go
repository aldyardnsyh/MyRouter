package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/store"
)

// settingKeys is the allow-list of mutable settings.
var settingKeys = map[string]bool{
	"base_url": true, "api_port": true, "cors_origins": true, "trusted_proxies": true,
	"session_ttl_hours": true, "default_retry": true, "default_timeout_ms": true,
	"log_retention_days": true, "store_request_body": true, "allow_private_upstreams": true,
	"timezone": true, "theme": true,
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request, _ *authSession) {
	values, err := s.db.AllSettings()
	if err != nil {
		s.log.Error("get settings: query failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "db_error", "could not load settings")
		return
	}
	defaults := map[string]string{
		"base_url": "", "api_port": "20128", "cors_origins": "", "trusted_proxies": "",
		"session_ttl_hours": "168", "default_retry": "1", "default_timeout_ms": "30000",
		"log_retention_days": "30", "store_request_body": "false", "allow_private_upstreams": "false",
		"timezone": "UTC", "theme": "system",
	}
	for k, v := range defaults {
		if _, ok := values[k]; !ok {
			values[k] = v
		}
	}
	// Never expose the master key.
	writeOK(w, map[string]any{"data": values, "version": s.version, "master_key_set": s.box != nil})
}

type settingsPayload map[string]string

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req settingsPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	clean := map[string]string{}
	for k, v := range req {
		if !settingKeys[k] {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "api_port":
			if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
				writeErr(w, http.StatusBadRequest, "invalid_input", "api_port must be 1-65535")
				return
			}
		case "base_url":
			if v != "" {
				u, err := url.Parse(v)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
					writeErr(w, http.StatusBadRequest, "invalid_input", "base_url must be an absolute http(s) URL without credentials")
					return
				}
			}
		case "cors_origins", "trusted_proxies":
			// Validate each entry; trusted proxies may be IPs/CIDRs/*.
			for _, entry := range store.SplitCSV(v) {
				if k == "cors_origins" && !strings.HasPrefix(entry, "http") {
					writeErr(w, http.StatusBadRequest, "invalid_input", "CORS origins must be full URLs")
					return
				}
			}
		}
		clean[k] = v
	}
	if err := s.db.SetSettings(clean); err != nil {
		// Log the detail, return a generic message: driver errors can contain
		// SQL fragments, file paths or other internal state (SEC: no info leak).
		s.log.Error("update settings: save failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "save_failed", "could not save settings")
		return
	}
	// Apply trusted-proxy changes live so Guard picks them up immediately.
	if v, ok := clean["trusted_proxies"]; ok {
		id := guard.NewIdentity(store.SplitCSV(v))
		s.identity = id
		s.gateway.SetIdentity(id)
	}
	// Apply the SSRF policy live too (SEC-06): the guarded transport reads this
	// flag on every dial, redirect and metadata check, so the new value takes
	// effect on the next upstream request instead of the next restart.
	if v, ok := clean["allow_private_upstreams"]; ok {
		allow := v == "true"
		s.allowPrivate = allow
		provider.SetAllowPrivate(allow)
		s.log.Info("settings: SSRF policy updated", logx.F("allow_private_upstreams", allow))
	}
	writeOK(w, map[string]bool{"ok": true})
}

var _ = model.StatusConnected
