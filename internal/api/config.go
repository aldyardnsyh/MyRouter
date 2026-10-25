package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/store"
)

// ---------------------------------------------------------------------------
// Configuration export / import
//
// A credential-free snapshot of the routing setup: providers (no API keys),
// models, routes and Guard policies. The file can be imported into a fresh
// instance to restore the configuration; credentials must be re-entered by
// hand because they never leave the vault.
// ---------------------------------------------------------------------------

type configProvider struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	Enabled bool   `json:"enabled"`
}

type configModel struct {
	ProviderName  string   `json:"provider_name"`
	ModelID       string   `json:"model_id"`
	DisplayName   string   `json:"display_name"`
	ContextWindow int      `json:"context_window"`
	PriceIn       *float64 `json:"price_in,omitempty"`
	PriceOut      *float64 `json:"price_out,omitempty"`
	Capabilities  string   `json:"capabilities"`
}

type configRoute struct {
	Alias          string   `json:"alias"`
	Description    string   `json:"description"`
	Enabled        bool     `json:"enabled"`
	Retry          int      `json:"retry"`
	TimeoutMS      int      `json:"timeout_ms"`
	FallbackOn     []string `json:"fallback_on"`
	PrimaryModel   string   `json:"primary_model"`
	FallbackModels []string `json:"fallback_models"`
}

type configPolicy struct {
	Name          string   `json:"name"`
	RPM           int      `json:"rpm"`
	TPM           int      `json:"tpm"`
	Burst         int      `json:"burst"`
	DailyTokens   int      `json:"daily_tokens"`
	MonthlyTokens int      `json:"monthly_tokens"`
	MaxConcurrent int      `json:"max_concurrent"`
	MaxTokensReq  int      `json:"max_tokens_per_request"`
	MaxIPs        int      `json:"max_ips"`
	MaxDevices    int      `json:"max_devices"`
	AllowedModels []string `json:"allowed_models"`
	OnExceed      string   `json:"on_exceed"`
	QueueMaxS     int      `json:"queue_max_s"`
	CooldownS     int      `json:"cooldown_s"`
	AutoBlockN    int      `json:"auto_block_n"`
	AutoBlockM    int      `json:"auto_block_m"`
	AutoBlockS    int      `json:"auto_block_s"`
}

// configPayload is the shape shared by the export file and the import body.
type configPayload struct {
	Version    string           `json:"version"`
	ExportedAt string           `json:"exported_at"`
	Providers  []configProvider `json:"providers"`
	Models     []configModel    `json:"models"`
	Routes     []configRoute    `json:"routes"`
	Policies   []configPolicy   `json:"policies"`
}

// RegisterConfigRoutes mounts the config export/import endpoints.
func (s *Server) RegisterConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/config/export", s.withAuth(s.handleExportConfig))
	mux.HandleFunc("POST /admin/config/import", s.withAuth(s.handleImportConfig))
}

// handleExportConfig returns the configuration WITHOUT any secret material: no
// credential ciphertext/nonce, no API-key hashes, no plaintext keys.
func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request, _ *authSession) {
	providers, err := s.db.ListProviders()
	if err != nil {
		s.log.Error("config export: list providers failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "db_error", "internal error")
		return
	}
	models, err := s.db.ListModels()
	if err != nil {
		s.log.Error("config export: list models failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "db_error", "internal error")
		return
	}
	routes, err := s.db.ListRoutes()
	if err != nil {
		s.log.Error("config export: list routes failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "db_error", "internal error")
		return
	}
	policies, err := s.db.ListPolicies()
	if err != nil {
		s.log.Error("config export: list policies failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "db_error", "internal error")
		return
	}

	// Routes reference models as "provider/model_id" so ids stay portable.
	byID := make(map[string]model.Model, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	ref := func(t model.RouteTarget) string {
		m, ok := byID[t.ModelID]
		if !ok {
			return t.ModelID
		}
		prov := m.ProviderName
		if prov == "" {
			prov = t.ProviderName
		}
		return prov + "/" + m.ModelID
	}

	out := configPayload{
		Version:    s.version,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Providers:  make([]configProvider, 0, len(providers)),
		Models:     make([]configModel, 0, len(models)),
		Routes:     make([]configRoute, 0, len(routes)),
		Policies:   make([]configPolicy, 0, len(policies)),
	}
	for _, p := range providers {
		out.Providers = append(out.Providers, configProvider{
			Name: p.Name, Type: p.Type, BaseURL: p.BaseURL, Enabled: p.Enabled,
		})
	}
	for _, m := range models {
		out.Models = append(out.Models, configModel{
			ProviderName: m.ProviderName, ModelID: m.ModelID, DisplayName: m.DisplayName,
			ContextWindow: m.ContextWindow, PriceIn: m.PriceIn, PriceOut: m.PriceOut,
			Capabilities: m.Capabilities,
		})
	}
	for _, rt := range routes {
		entry := configRoute{
			Alias: rt.Alias, Description: rt.Description, Enabled: rt.Enabled,
			Retry: rt.Retry, TimeoutMS: rt.TimeoutMS, FallbackOn: store.SplitCSV(rt.FallbackOn),
			FallbackModels: []string{},
		}
		if primary, ok := rt.Primary(); ok {
			entry.PrimaryModel = ref(primary)
		}
		for _, fb := range rt.Fallbacks() {
			entry.FallbackModels = append(entry.FallbackModels, ref(fb))
		}
		out.Routes = append(out.Routes, entry)
	}
	for _, p := range policies {
		out.Policies = append(out.Policies, configPolicy{
			Name: p.Name, RPM: p.RPM, TPM: p.TPM, Burst: p.Burst,
			DailyTokens: p.DailyTokens, MonthlyTokens: p.MonthlyTokens,
			MaxConcurrent: p.MaxConcurrent, MaxTokensReq: p.MaxTokensReq,
			MaxIPs: p.MaxIPs, MaxDevices: p.MaxDevices,
			AllowedModels: store.SplitCSV(p.AllowedModels),
			OnExceed:      p.OnExceed, QueueMaxS: p.QueueMaxS, CooldownS: p.CooldownS,
			AutoBlockN: p.AutoBlockN, AutoBlockM: p.AutoBlockM, AutoBlockS: p.AutoBlockS,
		})
	}
	writeOK(w, out)
}

// importCounts reports how many entries were imported per kind.
type importCounts struct {
	Providers int `json:"providers"`
	Models    int `json:"models"`
	Policies  int `json:"policies"`
	Routes    int `json:"routes"`
}

// importUpdated reports how many existing rows were updated, not created.
type importUpdated struct {
	Providers int `json:"providers"`
	Policies  int `json:"policies"`
	Routes    int `json:"routes"`
}

// handleImportConfig upserts providers (by name), policies (by name) and
// routes (by alias). Existing rows are UPDATED in place, never silently
// duplicated; the response documents how many were updated under "updated".
// Credentials are never imported: existing provider credentials are kept and
// new providers start without a key.
func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req configPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}

	counts, updated, conflicts, err := s.importConfig(&req)
	if err != nil {
		var ve *configError
		if errors.As(err, &ve) {
			writeErr(w, http.StatusBadRequest, "invalid_input", ve.Error())
			return
		}
		s.log.Error("config import failed", logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "import_failed", "import failed")
		return
	}
	writeOK(w, map[string]any{
		"providers": counts.Providers,
		"models":    counts.Models,
		"policies":  counts.Policies,
		"routes":    counts.Routes,
		"updated":   updated,
		"conflicts": conflicts,
	})
}

// configError marks a validation failure (client error, not a DB failure).
type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

func invalidInput(format string, args ...any) error {
	return &configError{msg: fmt.Sprintf(format, args...)}
}

// importConfig validates the whole payload first (so a bad entry cannot leave
// a half-applied import), then upserts providers, models, policies and routes.
func (s *Server) importConfig(req *configPayload) (importCounts, importUpdated, []string, error) {
	conflicts := []string{}
	var counts importCounts
	var updated importUpdated

	providers, err := s.db.ListProviders()
	if err != nil {
		return counts, updated, conflicts, err
	}
	models, err := s.db.ListModels()
	if err != nil {
		return counts, updated, conflicts, err
	}
	policies, err := s.db.ListPolicies()
	if err != nil {
		return counts, updated, conflicts, err
	}

	// ---- Validation pass -------------------------------------------------
	providerByName := make(map[string]*model.Provider, len(providers))
	for i := range providers {
		providerByName[strings.ToLower(providers[i].Name)] = &providers[i]
	}
	knownProviders := make(map[string]bool, len(providers)+len(req.Providers))
	for name := range providerByName {
		knownProviders[name] = true
	}

	type providerPlan struct {
		entry    configProvider
		existing *model.Provider
		baseURL  string
	}
	providerPlans := make([]providerPlan, 0, len(req.Providers))
	for _, cp := range req.Providers {
		name := strings.TrimSpace(cp.Name)
		if name == "" {
			return counts, updated, conflicts, invalidInput("provider name is required")
		}
		if !validTypes[cp.Type] {
			return counts, updated, conflicts, invalidInput("provider %q: unsupported type %q", name, cp.Type)
		}
		base := strings.TrimSpace(cp.BaseURL)
		if base == "" && s.registry != nil {
			base = s.registry.DefaultBaseURL(cp.Type)
		}
		validated, err := provider.ValidateBaseURL(base, s.allowPrivate)
		if err != nil {
			return counts, updated, conflicts, invalidInput("provider %q: %s", name, err.Error())
		}
		key := strings.ToLower(name)
		providerPlans = append(providerPlans, providerPlan{entry: cp, existing: providerByName[key], baseURL: validated})
		knownProviders[key] = true
	}

	for _, cm := range req.Models {
		if strings.TrimSpace(cm.ModelID) == "" {
			return counts, updated, conflicts, invalidInput("model_id is required")
		}
		if !knownProviders[strings.ToLower(strings.TrimSpace(cm.ProviderName))] {
			return counts, updated, conflicts, invalidInput("model %q references unknown provider %q", cm.ModelID, cm.ProviderName)
		}
	}

	policyByName := make(map[string]*model.Policy, len(policies))
	for i := range policies {
		policyByName[strings.ToLower(policies[i].Name)] = &policies[i]
	}
	for _, cp := range req.Policies {
		if strings.TrimSpace(cp.Name) == "" {
			return counts, updated, conflicts, invalidInput("policy name is required")
		}
	}

	// Projected model index: existing rows plus what this payload will add.
	projected := newModelIndex(models)
	for _, cm := range req.Models {
		projected.add(strings.TrimSpace(cm.ProviderName), strings.TrimSpace(cm.ModelID), "")
	}
	for _, cr := range req.Routes {
		alias := strings.ToLower(strings.TrimSpace(cr.Alias))
		if !aliasPattern.MatchString(alias) {
			return counts, updated, conflicts, invalidInput("route %q: alias must be 1-64 chars: letters, digits, dot, dash, underscore", cr.Alias)
		}
		if strings.TrimSpace(cr.PrimaryModel) == "" {
			return counts, updated, conflicts, invalidInput("route %q: primary_model is required", alias)
		}
		if err := projected.has(cr.PrimaryModel); err != nil {
			return counts, updated, conflicts, invalidInput("route %q: %s", alias, err.Error())
		}
		for _, fb := range cr.FallbackModels {
			if err := projected.has(fb); err != nil {
				return counts, updated, conflicts, invalidInput("route %q: %s", alias, err.Error())
			}
		}
	}

	// ---- Apply: providers -------------------------------------------------
	providerIDByName := make(map[string]string, len(providers)+len(providerPlans))
	for key, p := range providerByName {
		providerIDByName[key] = p.ID
	}
	for _, plan := range providerPlans {
		key := strings.ToLower(strings.TrimSpace(plan.entry.Name))
		if plan.existing != nil {
			// Never repoint an existing provider's base URL: its stored
			// credential would then be sent to an attacker-controlled host.
			// Only safe, non-destructive fields are applied on import.
			changed := plan.existing.Enabled != plan.entry.Enabled
			plan.existing.Enabled = plan.entry.Enabled
			if changed {
				if err := s.db.UpdateProvider(plan.existing); err != nil {
					return counts, updated, conflicts, err
				}
			}
			if !strings.EqualFold(strings.TrimRight(plan.existing.BaseURL, "/"), strings.TrimRight(plan.baseURL, "/")) {
				conflicts = append(conflicts, plan.entry.Name)
			}
			providerIDByName[key] = plan.existing.ID
			updated.Providers++
		} else {
			p := &model.Provider{
				ID: store.NewID("prv"), Name: strings.TrimSpace(plan.entry.Name), Type: plan.entry.Type,
				BaseURL: plan.baseURL, Status: model.StatusUnknown, Enabled: plan.entry.Enabled,
				CreatedAt: store.Now(),
			}
			if err := s.db.CreateProvider(p); err != nil {
				return counts, updated, conflicts, err
			}
			providerIDByName[key] = p.ID
		}
		counts.Providers++
	}

	// ---- Apply: models ----------------------------------------------------
	for _, cm := range req.Models {
		pid := providerIDByName[strings.ToLower(strings.TrimSpace(cm.ProviderName))]
		m := &model.Model{
			ProviderID: pid, ModelID: strings.TrimSpace(cm.ModelID),
			DisplayName: cm.DisplayName, ContextWindow: cm.ContextWindow,
			PriceIn: cm.PriceIn, PriceOut: cm.PriceOut,
			Capabilities: cm.Capabilities, Available: true,
		}
		if _, err := s.db.UpsertModel(m); err != nil {
			return counts, updated, conflicts, err
		}
		counts.Models++
	}

	// ---- Apply: policies --------------------------------------------------
	for _, cp := range req.Policies {
		name := strings.TrimSpace(cp.Name)
		pl := &model.Policy{
			Name: name, RPM: cp.RPM, TPM: cp.TPM, Burst: cp.Burst,
			DailyTokens: cp.DailyTokens, MonthlyTokens: cp.MonthlyTokens,
			MaxConcurrent: cp.MaxConcurrent, MaxTokensReq: cp.MaxTokensReq,
			MaxIPs: cp.MaxIPs, MaxDevices: cp.MaxDevices,
			AllowedModels: store.JoinCSV(cp.AllowedModels),
			OnExceed:      cp.OnExceed, QueueMaxS: cp.QueueMaxS, CooldownS: cp.CooldownS,
			AutoBlockN: cp.AutoBlockN, AutoBlockM: cp.AutoBlockM, AutoBlockS: cp.AutoBlockS,
		}
		switch pl.OnExceed {
		case "reject", "queue", "fallback":
		default:
			pl.OnExceed = "reject"
		}
		if existing := policyByName[strings.ToLower(name)]; existing != nil {
			pl.ID = existing.ID
			pl.CreatedAt = existing.CreatedAt
			updated.Policies++
		}
		if err := s.db.UpsertPolicy(pl); err != nil {
			return counts, updated, conflicts, err
		}
		counts.Policies++
	}

	// ---- Apply: routes ----------------------------------------------------
	// Re-index with the real DB ids now that models exist.
	allModels, err := s.db.ListModels()
	if err != nil {
		return counts, updated, conflicts, err
	}
	index := newModelIndex(allModels)
	for _, cr := range req.Routes {
		alias := strings.ToLower(strings.TrimSpace(cr.Alias))
		primaryID, err := index.resolve(cr.PrimaryModel)
		if err != nil {
			return counts, updated, conflicts, invalidInput("route %q: %s", alias, err.Error())
		}
		targets := []model.RouteTarget{{ModelID: primaryID, Role: "primary", Position: 0}}
		seen := map[string]bool{primaryID: true}
		pos := 1
		for _, fb := range cr.FallbackModels {
			fbID, err := index.resolve(fb)
			if err != nil {
				return counts, updated, conflicts, invalidInput("route %q: %s", alias, err.Error())
			}
			if seen[fbID] {
				continue
			}
			seen[fbID] = true
			targets = append(targets, model.RouteTarget{ModelID: fbID, Role: "fallback", Position: pos})
			pos++
		}

		rt := &model.Route{
			Alias: alias, Description: strings.TrimSpace(cr.Description), Enabled: cr.Enabled,
			Strategy: "priority", Retry: cr.Retry, TimeoutMS: cr.TimeoutMS,
			FallbackOn: store.JoinCSV(cr.FallbackOn), Targets: targets,
		}
		if rt.Retry <= 0 {
			rt.Retry = 1
		}
		if rt.TimeoutMS <= 0 {
			rt.TimeoutMS = 30000
		}
		if rt.FallbackOn == "" {
			rt.FallbackOn = "rate_limit,timeout,upstream_5xx,network"
		}
		if existing, err := s.db.RouteByAlias(alias); err == nil {
			rt.ID = existing.ID
			rt.CreatedAt = existing.CreatedAt
			rt.Strategy = existing.Strategy
			rt.PolicyID = existing.PolicyID
			updated.Routes++
		}
		if err := s.db.SaveRoute(rt); err != nil {
			return counts, updated, conflicts, err
		}
		counts.Routes++
	}
	return counts, updated, conflicts, nil
}

// modelIndex resolves a portable model reference ("provider/model_id" or a
// bare model_id) to the local model row id.
type modelIndex struct {
	byProviderModel map[string]string   // "provider|model_id" -> id ("" = projected)
	byModelID       map[string][]string // model_id -> ids
	providers       []string            // lowercased provider names, longest first
}

func newModelIndex(models []model.Model) *modelIndex {
	idx := &modelIndex{byProviderModel: map[string]string{}, byModelID: map[string][]string{}}
	for _, m := range models {
		idx.add(m.ProviderName, m.ModelID, m.ID)
	}
	return idx
}

// add registers a model. Pass an empty id for a model that the payload is
// about to create: it then only satisfies reference validation.
func (idx *modelIndex) add(providerName, modelID, id string) {
	name := strings.ToLower(strings.TrimSpace(providerName))
	key := name + "|" + strings.ToLower(modelID)
	if _, exists := idx.byProviderModel[key]; !exists {
		idx.byProviderModel[key] = id
	}
	if name != "" && !containsString(idx.providers, name) {
		idx.providers = append(idx.providers, name)
	}
	if id != "" {
		idx.byModelID[modelID] = append(idx.byModelID[modelID], id)
	}
}

// lookup resolves a reference; found reports whether the model is known.
// Model ids may themselves contain slashes ("meta-llama/Llama-3"), so every
// known provider name is tried as a prefix before falling back to a bare id.
func (idx *modelIndex) lookup(ref string) (id string, found bool, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false, &configError{msg: "model reference is required"}
	}
	lower := strings.ToLower(ref)
	for _, prov := range idx.providers {
		mid, ok := strings.CutPrefix(lower, prov+"/")
		if !ok {
			continue
		}
		if id, ok := idx.byProviderModel[prov+"|"+mid]; ok {
			return id, true, nil
		}
	}
	ids := idx.byModelID[ref]
	switch len(ids) {
	case 1:
		return ids[0], true, nil
	case 0:
		return "", false, nil
	default:
		return "", false, &configError{msg: "model " + ref + ` is ambiguous across providers, use "provider/model_id"`}
	}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// has reports whether a reference can be satisfied (validation pass).
func (idx *modelIndex) has(ref string) error {
	_, found, err := idx.lookup(ref)
	if err != nil {
		return err
	}
	if !found {
		return &configError{msg: "unknown model " + strings.TrimSpace(ref)}
	}
	return nil
}

// resolve returns the concrete local model id (apply pass).
func (idx *modelIndex) resolve(ref string) (string, error) {
	id, found, err := idx.lookup(ref)
	if err != nil {
		return "", err
	}
	if !found {
		return "", &configError{msg: "unknown model " + strings.TrimSpace(ref)}
	}
	if id == "" {
		return "", &configError{msg: "model " + strings.TrimSpace(ref) + " was not created"}
	}
	return id, nil
}
