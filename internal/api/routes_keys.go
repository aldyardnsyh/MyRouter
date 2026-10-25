package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

var aliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListRoutes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": list})
}

type routePayload struct {
	Alias       string   `json:"alias"`
	Description string   `json:"description"`
	Enabled     *bool    `json:"enabled"`
	Strategy    string   `json:"strategy"`
	Retry       *int     `json:"retry"`
	TimeoutMS   *int     `json:"timeout_ms"`
	PolicyID    *string  `json:"policy_id"`
	FallbackOn  []string `json:"fallback_on"`
	Primary     string   `json:"primary_model_id"`
	Fallbacks   []string `json:"fallback_model_ids"`
}

func (p routePayload) fallbackCSV() string {
	if len(p.FallbackOn) == 0 {
		return "rate_limit,timeout,upstream_5xx,network"
	}
	return store.JoinCSV(p.FallbackOn)
}

func (s *Server) handleCreateRoute(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req routePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	req.Alias = strings.TrimSpace(strings.ToLower(req.Alias))
	if !aliasPattern.MatchString(req.Alias) {
		writeErr(w, http.StatusBadRequest, "invalid_input", "alias must be 1-64 chars: letters, digits, dot, dash, underscore")
		return
	}
	if strings.TrimSpace(req.Primary) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "primary model is required")
		return
	}
	// Validate model references before touching the DB: route_target.model_id
	// is a foreign key, and an unknown id would otherwise surface as a raw
	// 500 save_failed instead of a meaningful client error.
	ids := []string{strings.TrimSpace(req.Primary)}
	ids = append(ids, req.Fallbacks...)
	validated := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || validated[id] {
			continue
		}
		if _, err := s.db.ModelByID(id); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_input",
				fmt.Sprintf("unknown model id %q: run model discovery on the provider first", id))
			return
		}
		validated[id] = true
	}
	if _, err := s.db.RouteByAlias(req.Alias); err == nil {
		writeErr(w, http.StatusConflict, "duplicate_alias", "a route with this alias already exists")
		return
	}
	rt := &model.Route{
		Alias: req.Alias, Description: strings.TrimSpace(req.Description), Enabled: true,
		Strategy: "priority", Retry: 1, TimeoutMS: 30000, FallbackOn: req.fallbackCSV(), PolicyID: req.PolicyID,
	}
	if req.Enabled != nil {
		rt.Enabled = *req.Enabled
	}
	if req.Retry != nil {
		rt.Retry = *req.Retry
	}
	if req.TimeoutMS != nil {
		rt.TimeoutMS = *req.TimeoutMS
	}
	if req.Strategy != "" {
		rt.Strategy = req.Strategy
	}

	targets := []model.RouteTarget{{ModelID: req.Primary, Role: "primary", Position: 0}}
	seen := map[string]bool{req.Primary: true}
	pos := 1
	for _, fb := range req.Fallbacks {
		if fb == "" || seen[fb] {
			continue
		}
		seen[fb] = true
		targets = append(targets, model.RouteTarget{ModelID: fb, Role: "fallback", Position: pos})
		pos++
	}
	rt.Targets = targets
	if err := s.db.SaveRoute(rt); err != nil {
		writeErr(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if req.PolicyID != nil && *req.PolicyID != "" {
		_ = s.db.BindPolicy(*req.PolicyID, "route", rt.ID)
	} else {
		_ = s.db.BindPolicy("", "route", rt.ID)
	}
	saved, _ := s.db.RouteByID(rt.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"data": saved})
}

func (s *Server) handleUpdateRoute(w http.ResponseWriter, r *http.Request, _ *authSession) {
	existing, err := s.db.RouteByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	var req routePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	rt := existing
	if req.Alias != "" {
		alias := strings.TrimSpace(strings.ToLower(req.Alias))
		if !aliasPattern.MatchString(alias) {
			writeErr(w, http.StatusBadRequest, "invalid_input", "invalid alias")
			return
		}
		if alias != existing.Alias {
			if other, err := s.db.RouteByAlias(alias); err == nil && other.ID != existing.ID {
				writeErr(w, http.StatusConflict, "duplicate_alias", "a route with this alias already exists")
				return
			}
		}
		rt.Alias = alias
	}
	rt.Description = strings.TrimSpace(req.Description)
	if req.Enabled != nil {
		rt.Enabled = *req.Enabled
	}
	if req.Retry != nil {
		rt.Retry = *req.Retry
	}
	if req.TimeoutMS != nil {
		rt.TimeoutMS = *req.TimeoutMS
	}
	if req.Strategy != "" {
		rt.Strategy = req.Strategy
	}
	if len(req.FallbackOn) > 0 {
		rt.FallbackOn = req.fallbackCSV()
	}
	if req.PolicyID != nil {
		if *req.PolicyID == "" {
			rt.PolicyID = nil
		} else {
			rt.PolicyID = req.PolicyID
		}
	}
	if strings.TrimSpace(req.Primary) != "" {
		ids := []string{strings.TrimSpace(req.Primary)}
		ids = append(ids, req.Fallbacks...)
		validated := make(map[string]bool, len(ids))
		for _, id := range ids {
			if id == "" || validated[id] {
				continue
			}
			if _, err := s.db.ModelByID(id); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_input",
					fmt.Sprintf("unknown model id %q: run model discovery on the provider first", id))
				return
			}
			validated[id] = true
		}
		targets := []model.RouteTarget{{ModelID: req.Primary, Role: "primary", Position: 0}}
		seen := map[string]bool{req.Primary: true}
		pos := 1
		for _, fb := range req.Fallbacks {
			if fb == "" || seen[fb] {
				continue
			}
			seen[fb] = true
			targets = append(targets, model.RouteTarget{ModelID: fb, Role: "fallback", Position: pos})
			pos++
		}
		rt.Targets = targets
	}
	if err := s.db.SaveRoute(rt); err != nil {
		writeErr(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if req.PolicyID != nil {
		_ = s.db.BindPolicy(*req.PolicyID, "route", rt.ID)
	}
	// API keys store allowed routes by alias. Renaming a route without
	// rewriting those CSVs silently revokes access for every restricted key.
	if rt.Alias != existing.Alias {
		s.rewriteAllowedRoutes(existing.Alias, rt.Alias)
	}
	saved, _ := s.db.RouteByID(rt.ID)
	writeOK(w, map[string]any{"data": saved})
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request, _ *authSession) {
	if err := s.db.DeleteRoute(s.pathID(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListAPIKeys()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": list})
}

type apiKeyPayload struct {
	Name          string   `json:"name"`
	Enabled       *bool    `json:"enabled"`
	ExpiresAt     *int64   `json:"expires_at"`
	PolicyID      *string  `json:"policy_id"`
	AllowedRoutes []string `json:"allowed_routes"`
}

// handleCreateAPIKey issues a key. The secret is returned exactly once.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req apiKeyPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "name is required")
		return
	}
	secretPart := "mr-sk-" + store.RandomHex(24)
	prefix := secretPart[:12]
	key := &model.APIKey{
		ID: store.NewID("key"), Name: req.Name, Prefix: prefix,
		Hash: store.HashToken(secretPart), Enabled: true, CreatedAt: store.Now(),
		ExpiresAt: req.ExpiresAt, PolicyID: req.PolicyID, AllowedRoutes: store.JoinCSV(req.AllowedRoutes),
	}
	if req.Enabled != nil {
		key.Enabled = *req.Enabled
	}
	if err := s.db.CreateAPIKey(key); err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	if req.PolicyID != nil && *req.PolicyID != "" {
		_ = s.db.BindPolicy(*req.PolicyID, "api_key", key.ID)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"data": key,
		// Returned once; never retrievable again.
		"secret": secretPart,
	})
}

func (s *Server) handleUpdateAPIKey(w http.ResponseWriter, r *http.Request, _ *authSession) {
	keys, err := s.db.ListAPIKeys()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	var target *model.APIKey
	for i := range keys {
		if keys[i].ID == s.pathID(r) {
			target = &keys[i]
		}
	}
	if target == nil {
		writeErr(w, http.StatusNotFound, "not_found", "API key not found")
		return
	}
	var req apiKeyPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if req.Name != "" {
		target.Name = strings.TrimSpace(req.Name)
	}
	if req.Enabled != nil {
		target.Enabled = *req.Enabled
	}
	if req.ExpiresAt != nil {
		target.ExpiresAt = req.ExpiresAt
	}
	if req.PolicyID != nil {
		if *req.PolicyID == "" {
			target.PolicyID = nil
		} else {
			target.PolicyID = req.PolicyID
		}
		_ = s.db.BindPolicy(*req.PolicyID, "api_key", target.ID)
	}
	target.AllowedRoutes = store.JoinCSV(req.AllowedRoutes)
	if err := s.db.UpdateAPIKey(target); err != nil {
		writeErr(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": target})
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request, _ *authSession) {
	if err := s.db.DeleteAPIKey(s.pathID(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "revoke_failed", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// rewriteAllowedRoutes replaces an old alias with the new one in every API key
// allowlist. Matching is case-insensitive to mirror the gateway check.
func (s *Server) rewriteAllowedRoutes(oldAlias, newAlias string) {
	keys, err := s.db.ListAPIKeys()
	if err != nil {
		return
	}
	for i := range keys {
		parts := store.SplitCSV(keys[i].AllowedRoutes)
		changed := false
		for j, alias := range parts {
			if strings.EqualFold(alias, oldAlias) {
				parts[j] = newAlias
				changed = true
			}
		}
		if changed {
			keys[i].AllowedRoutes = store.JoinCSV(parts)
			_ = s.db.UpdateAPIKey(&keys[i])
		}
	}
}
