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
	Effort      string   `json:"effort"`
	PolicyID    *string  `json:"policy_id"`
	FallbackOn  []string `json:"fallback_on"`
	Primary     string   `json:"primary_model_id"`
	Fallbacks   []string `json:"fallback_model_ids"`
}

// resolvePolicyRef turns an optional policy id into a nullable reference.
// The admin UI sends "" for "global / inherit", which must become NULL rather
// than an empty-string foreign key.
func (s *Server) resolvePolicyRef(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	id := strings.TrimSpace(*raw)
	if id == "" {
		return nil, nil
	}
	if _, err := s.db.PolicyByID(id); err != nil {
		return nil, fmt.Errorf("unknown policy id %q: select an existing policy or leave it empty", id)
	}
	return &id, nil
}

func (p routePayload) fallbackCSV() string {
	if len(p.FallbackOn) == 0 {
		return "rate_limit,timeout,upstream_5xx,network,billing"
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
	// An empty policy_id from the UI means "global / inherit", not a policy
	// with an empty id. Storing "" used to trip the policy foreign key.
	policyRef, err := s.resolvePolicyRef(req.PolicyID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
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
		Strategy: "priority", Retry: 1, TimeoutMS: 30000, Effort: defaultEffort(req.Effort), FallbackOn: req.fallbackCSV(), PolicyID: policyRef,
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
	if policyRef != nil {
		_ = s.db.BindPolicy(*policyRef, "route", rt.ID)
	} else {
		_ = s.db.BindPolicy("", "route", rt.ID)
	}
	saved, _ := s.db.RouteByID(rt.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"data": saved})
}

// normalizeEffort validates the adaptive reasoning budget per route.
// Empty means "not provided" (caller keeps existing/default); unknown values
// fall back to medium.
func normalizeEffort(e string) string {
	switch strings.ToLower(strings.TrimSpace(e)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(e))
	case "":
		return ""
	default:
		return "medium"
	}
}

// defaultEffort resolves the effort for new routes: explicit value or medium.
func defaultEffort(e string) string {
	if v := normalizeEffort(e); v != "" {
		return v
	}
	return "medium"
}

// autoAlias converts a provider model id into a valid route alias: lowercase,
// invalid chars collapsed to dashes, trimmed to the alias pattern's 64 chars.
// Suffixes -2, -3… are appended when the alias is taken, so a single bulk run
// never fails on collisions (e.g. the same model id on two providers).
func autoAlias(modelID string, taken func(string) bool) string {
	base := strings.ToLower(strings.TrimSpace(modelID))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	alias := strings.Trim(b.String(), "-")
	if alias == "" {
		alias = "model"
	}
	if len(alias) > 64 {
		alias = alias[:64]
	}
	alias = strings.TrimRight(alias, "-.")
	if !aliasPattern.MatchString(alias) {
		alias = "model"
	}
	if !taken(alias) {
		return alias
	}
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d", alias, i)
		if len(cand) > 64 {
			cand = cand[:64]
		}
		if !taken(cand) {
			return cand
		}
	}
	return ""
}

// defaultEffortForModel picks the adaptive reasoning budget for auto-created
// routes. Lightweight heuristics on the model id only (never a hardcoded
// route name): code/reasoning-flavoured models default to high, embedding or
// tiny models to low, everything else medium. User can override per route.
func defaultEffortForModel(modelID string) string {
	lower := strings.ToLower(modelID)
	for _, kw := range []string{"code", "coder", "reason", "think", "opus", "pro", "ultra", "70b", "405b"} {
		if strings.Contains(lower, kw) {
			return "high"
		}
	}
	for _, kw := range []string{"embed", "mini", "nano", "tiny", "small", "haiku", "flash-lite", "1b", "3b", "7b"} {
		if strings.Contains(lower, kw) {
			return "low"
		}
	}
	return "medium"
}

// autoRoutePayload lets the operator pick exactly which discovered models get
// a route. A missing field (nil slice) means "every eligible model", while an
// explicitly empty list is a client error: clearing the selection and pressing
// Add must never silently create one route per model.
type autoRoutePayload struct {
	ModelIDs []string `json:"model_ids"`
}

// handleAutoRoutes creates one route per selected discovered model with
// sensible defaults (alias = model id, primary = that model, global policy).
// Models that already own a route or whose provider is disabled/unavailable
// are skipped, so the action is idempotent and safe to click repeatedly after
// adding new providers or re-running model discovery.
func (s *Server) handleAutoRoutes(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req autoRoutePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	selected := make(map[string]bool, len(req.ModelIDs))
	for _, id := range req.ModelIDs {
		if id = strings.TrimSpace(id); id != "" {
			selected[id] = true
		}
	}
	if req.ModelIDs != nil && len(selected) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_input", "select at least one model to add")
		return
	}

	// A model can only be reached through an enabled provider. The store folds
	// model.available separately, so both flags are checked here.
	providers, err := s.db.ListProviders()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	providerEnabled := make(map[string]bool, len(providers))
	for _, p := range providers {
		providerEnabled[p.ID] = p.Enabled
	}
	// Skip models that already have a route: without this, running auto-add
	// again would create a second alias pointing at the same model.
	existingRoutes, err := s.db.ListRoutes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// Any target counts as routed, not just the primary: a model used as a
	// fallback already has a path to clients, and creating a second alias for
	// it would duplicate the same upstream.
	routed := make(map[string]bool, len(existingRoutes))
	for _, rt := range existingRoutes {
		for _, target := range rt.Targets {
			routed[target.ModelID] = true
		}
	}

	models, err := s.db.ListModels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	created := make([]*model.Route, 0, len(models))
	skipped := 0
	for _, m := range models {
		if len(selected) > 0 && !selected[m.ID] {
			continue
		}
		if !m.Available || !providerEnabled[m.ProviderID] || routed[m.ID] {
			skipped++
			continue
		}
		alias := autoAlias(m.ModelID, func(a string) bool {
			if _, err := s.db.RouteByAlias(a); err == nil {
				return true
			}
			for _, c := range created {
				if c.Alias == a {
					return true
				}
			}
			return false
		})
		if alias == "" {
			skipped++
			continue
		}
		prov := m.ProviderName
		if prov == "" {
			prov = m.ProviderID
		}
		rt := &model.Route{
			Alias:       alias,
			Description: fmt.Sprintf("Auto-added · %s", prov),
			Enabled:     true,
			Strategy:    "priority",
			Retry:       1,
			TimeoutMS:   30000,
			Effort:      defaultEffortForModel(m.ModelID),
			FallbackOn:  "rate_limit,timeout,upstream_5xx,network",
			Targets:     []model.RouteTarget{{ModelID: m.ID, Role: "primary", Position: 0}},
		}
		if err := s.db.SaveRoute(rt); err != nil {
			writeErr(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
		routed[m.ID] = true
		created = append(created, rt)
	}
	writeOK(w, map[string]any{"created": len(created), "skipped": skipped, "data": created})
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
	if req.Effort != "" {
		rt.Effort = normalizeEffort(req.Effort)
	}
	if len(req.FallbackOn) > 0 {
		rt.FallbackOn = req.fallbackCSV()
	}
	if req.PolicyID != nil {
		policyRef, err := s.resolvePolicyRef(req.PolicyID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		rt.PolicyID = policyRef
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
		if rt.PolicyID != nil {
			_ = s.db.BindPolicy(*rt.PolicyID, "route", rt.ID)
		} else {
			_ = s.db.BindPolicy("", "route", rt.ID)
		}
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

const maxBulkRoutes = 500

type bulkRoutePayload struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
}

// handleBulkRoutes applies one action to a set of routes. The Routes page uses
// it for multi-select enable/disable/delete so a large cleanup no longer needs
// one request per row.
func (s *Server) handleBulkRoutes(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req bulkRoutePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	ids := make([]string, 0, len(req.IDs))
	seen := make(map[string]bool, len(req.IDs))
	for _, id := range req.IDs {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_input", "select at least one route")
		return
	}
	if len(ids) > maxBulkRoutes {
		writeErr(w, http.StatusBadRequest, "invalid_input",
			fmt.Sprintf("too many routes in one request (max %d)", maxBulkRoutes))
		return
	}
	switch req.Action {
	case "enable", "disable":
		n, err := s.db.SetRoutesEnabled(ids, req.Action == "enable")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "update_failed", err.Error())
			return
		}
		writeOK(w, map[string]any{"updated": n})
	case "delete":
		n, err := s.db.DeleteRoutes(ids)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
			return
		}
		writeOK(w, map[string]any{"deleted": n})
	default:
		writeErr(w, http.StatusBadRequest, "invalid_input", "action must be enable, disable, or delete")
	}
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
	policyRef, err := s.resolvePolicyRef(req.PolicyID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	secretPart := "mr-sk-" + store.RandomHex(24)
	prefix := secretPart[:12]
	key := &model.APIKey{
		ID: store.NewID("key"), Name: req.Name, Prefix: prefix,
		Hash: store.HashToken(secretPart), Enabled: true, CreatedAt: store.Now(),
		ExpiresAt: req.ExpiresAt, PolicyID: policyRef, AllowedRoutes: store.JoinCSV(req.AllowedRoutes),
	}
	if req.Enabled != nil {
		key.Enabled = *req.Enabled
	}
	if err := s.db.CreateAPIKey(key); err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	if policyRef != nil {
		_ = s.db.BindPolicy(*policyRef, "api_key", key.ID)
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
		policyRef, err := s.resolvePolicyRef(req.PolicyID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		target.PolicyID = policyRef
		if policyRef != nil {
			_ = s.db.BindPolicy(*policyRef, "api_key", target.ID)
		} else {
			_ = s.db.BindPolicy("", "api_key", target.ID)
		}
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
