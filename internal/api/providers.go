package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/modelmeta"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListProviders()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": list})
}

func (s *Server) handleGetProvider(w http.ResponseWriter, r *http.Request, _ *authSession) {
	p, err := s.db.ProviderByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	writeOK(w, map[string]any{"data": p})
}

type providerPayload struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Enabled *bool  `json:"enabled"`
	// ConfirmBaseURL acknowledges that the stored credential will be sent to a
	// different host. Required when base_url changes on a provider that already
	// has a credential and no replacement key is supplied (SEC-07).
	ConfirmBaseURL bool `json:"confirm_base_url"`
}

var validTypes = map[string]bool{
	model.ProviderOpenAI: true, model.ProviderAnthropic: true, model.ProviderGemini: true,
	model.ProviderDeepSeek: true, model.ProviderCompatible: true,
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req providerPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "name is required")
		return
	}
	if !validTypes[req.Type] {
		writeErr(w, http.StatusBadRequest, "invalid_input", "unsupported provider type")
		return
	}
	baseURL := req.BaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = s.registry.DefaultBaseURL(req.Type)
	}
	validated, err := provider.ValidateBaseURL(baseURL, s.allowPrivate)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unsafe_url", err.Error())
		return
	}
	p := &model.Provider{
		ID: store.NewID("prv"), Name: req.Name, Type: req.Type, BaseURL: validated,
		Status: model.StatusUnknown, Enabled: true, CreatedAt: store.Now(),
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if err := s.db.CreateProvider(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	if strings.TrimSpace(req.APIKey) != "" {
		if err := s.saveCredential(p.ID, req.APIKey); err != nil {
			writeErr(w, http.StatusInternalServerError, "credential_failed", err.Error())
			return
		}
	}
	// Re-fetch so the masked credential hint is included.
	created, err := s.db.ProviderByID(p.ID)
	if err != nil {
		created = p
	}
	// Automation: a saved provider is probed immediately, and its models are
	// imported so Routes can be created without any extra clicks.
	models, conn := 0, providerConnection{Status: created.Status}
	if created.Enabled && strings.TrimSpace(req.APIKey) != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		models, conn = s.discoverModels(ctx, created)
		cancel()
		if refreshed, err := s.db.ProviderByID(p.ID); err == nil {
			created = refreshed
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"data": created, "connection": conn, "models": models,
	})
}

// providerConnection is the result of the automatic connection probe returned
// with provider create/update responses.
type providerConnection struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latency_ms"`
}

// providerStatusFor maps an upstream error category to a provider status badge.
func providerStatusFor(pe *provider.Error) string {
	switch pe.Category {
	case provider.CatRateLimit:
		return model.StatusRateLimited
	case provider.CatAuth:
		return model.StatusInvalidCred
	case provider.CatTimeout:
		return model.StatusDegraded
	default:
		return model.StatusUnavailable
	}
}

// discoverModels validates the provider credential by listing upstream models,
// imports/enriches them, and records the resulting status. It is the shared
// path behind "Save provider", "Test connection + models" automation, the
// manual refresh button and the periodic probe.
func (s *Server) discoverModels(ctx context.Context, p *model.Provider) (int, providerConnection) {
	start := time.Now()
	conn := providerConnection{Status: model.StatusConnected}
	apiKey, err := s.decrypt(p.ID)
	if err != nil {
		conn.Status, conn.Message = model.StatusInvalidCred, "credential unavailable"
		_ = s.db.SetProviderStatus(p.ID, conn.Status, conn.Message, store.Now())
		conn.LatencyMS = time.Since(start).Milliseconds()
		return 0, conn
	}
	if apiKey == "" {
		conn.Status, conn.Message = model.StatusInvalidCred, "no API key configured"
		_ = s.db.SetProviderStatus(p.ID, conn.Status, conn.Message, store.Now())
		conn.LatencyMS = time.Since(start).Milliseconds()
		return 0, conn
	}
	infos, err := s.adapterFor(p).ListModels(ctx, provider.Credentials{APIKey: apiKey, BaseURL: p.BaseURL})
	if err != nil {
		pe := provider.AsError(err)
		conn.Status, conn.Message = providerStatusFor(pe), pe.Message
		_ = s.db.SetProviderStatus(p.ID, conn.Status, conn.Message, store.Now())
		conn.LatencyMS = time.Since(start).Milliseconds()
		return 0, conn
	}
	ids := make([]string, 0, len(infos))
	for i := range infos {
		info := infos[i]
		display := strings.TrimSpace(info.DisplayName)
		if display == "" {
			display = info.ModelID
		}
		mdl := &model.Model{
			ProviderID: p.ID, ModelID: info.ModelID, DisplayName: display,
			ContextWindow: info.ContextWindow, PriceIn: info.PriceIn, PriceOut: info.PriceOut,
			Capabilities: store.JoinCSV(info.Capabilities), Available: true,
		}
		// Official metadata (context window, prices, capabilities) wins over
		// whatever the provider endpoint reported.
		modelmeta.Apply(s.catalog, p, mdl)
		id, err := s.db.UpsertModel(mdl)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	_ = s.db.DeleteModelsExcept(p.ID, ids)
	_ = s.db.SetProviderStatus(p.ID, model.StatusConnected, "", store.Now())
	conn.LatencyMS = time.Since(start).Milliseconds()
	return len(ids), conn
}

func (s *Server) saveCredential(providerID, apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	ct, nonce, err := s.box.Encrypt(apiKey)
	if err != nil {
		return err
	}
	return s.db.SaveCredential(providerID, ct, nonce, s.box.Version(), secret.Mask(apiKey))
}

// hasStoredCredential reports whether a credential is already encrypted for the
// provider. A missing row is not an error: it simply means nothing can leak.
func (s *Server) hasStoredCredential(providerID string) (bool, error) {
	_, _, err := s.db.CredentialFor(providerID)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// normalizeBaseURL trims surrounding space and trailing slashes so two spellings
// of the same endpoint compare equal.
func normalizeBaseURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request, _ *authSession) {
	p, err := s.db.ProviderByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	var req providerPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}

	// SEC-07: repointing base_url while keeping the stored credential would hand
	// that credential to whatever host the new URL names. Every check below runs
	// before the first write, so a rejected request changes nothing at all.
	baseURLChanged := false
	newBaseURL := ""
	if strings.TrimSpace(req.BaseURL) != "" {
		validated, err := provider.ValidateBaseURL(req.BaseURL, s.allowPrivate)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "unsafe_url", err.Error())
			return
		}
		newBaseURL = validated
		baseURLChanged = !strings.EqualFold(normalizeBaseURL(validated), normalizeBaseURL(p.BaseURL))
		if baseURLChanged {
			// A replacement key makes the old credential irrelevant, so no
			// confirmation is needed in that case.
			if strings.TrimSpace(req.APIKey) == "" {
				hasCred, err := s.hasStoredCredential(p.ID)
				if err != nil {
					s.log.Error("provider credential lookup failed",
						logx.F("provider", p.ID), logx.F("error", err.Error()))
					writeErr(w, http.StatusInternalServerError, "credential_error", "could not read stored credential")
					return
				}
				if hasCred && !req.ConfirmBaseURL {
					writeErr(w, http.StatusBadRequest, "base_url_change_requires_confirmation",
						"changing base_url would send the stored API key to a different host; "+
							"send confirm_base_url=true to acknowledge this, or supply a new api_key")
					return
				}
			}
		}
		p.BaseURL = newBaseURL
	}
	if req.Name != "" {
		p.Name = strings.TrimSpace(req.Name)
	}
	if req.Type != "" && validTypes[req.Type] {
		p.Type = req.Type
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if err := s.db.UpdateProvider(p); err != nil {
		s.log.Error("provider update failed", logx.F("provider", p.ID), logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "update_failed", "could not update provider")
		return
	}
	if baseURLChanged {
		// Audit trail: the URL is not a secret, the credential never is logged.
		s.log.Warn("provider base_url changed",
			logx.F("provider", p.ID), logx.F("base_url", newBaseURL))
	}
	keyChanged := strings.TrimSpace(req.APIKey) != ""
	if keyChanged {
		if err := s.saveCredential(p.ID, req.APIKey); err != nil {
			s.log.Error("provider credential save failed", logx.F("provider", p.ID), logx.F("error", err.Error()))
			writeErr(w, http.StatusInternalServerError, "credential_failed", "could not save credential")
			return
		}
	}
	updated, _ := s.db.ProviderByID(p.ID)
	if updated == nil {
		updated = p
	}
	// Automation: every edit of an enabled provider re-probes and re-imports
	// models so the response always reflects the real connection state and the
	// model list stays fresh. Disabled providers are left untouched.
	models, conn := 0, providerConnection{Status: updated.Status}
	needProbe := updated.Enabled
	if needProbe {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		models, conn = s.discoverModels(ctx, updated)
		cancel()
		if refreshed, err := s.db.ProviderByID(p.ID); err == nil {
			updated = refreshed
		}
	} else if stored, err := s.db.ListModelsByProvider(p.ID); err == nil {
		// Disabled providers are not probed; report their stored models so the
		// client does not interpret "0" as a connection failure.
		models = len(stored)
	}
	writeOK(w, map[string]any{"data": updated, "connection": conn, "models": models})
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request, _ *authSession) {
	routes, err := s.db.DeleteProvider(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true, "routes_removed": routes})
}

// handleTestProvider validates credentials against the upstream.
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request, _ *authSession) {
	p, err := s.db.ProviderByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if !p.Enabled {
		writeErr(w, http.StatusConflict, "provider_disabled", "provider is disabled; enable it before testing")
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	apiKey, decErr := s.decrypt(p.ID)
	status, msg := model.StatusConnected, ""
	if decErr != nil {
		status, msg = model.StatusInvalidCred, "credential unavailable"
	} else if apiKey == "" {
		status, msg = model.StatusInvalidCred, "no API key configured"
	} else if err := s.adapterFor(p).Validate(ctx, provider.Credentials{APIKey: apiKey, BaseURL: p.BaseURL}); err != nil {
		pe := provider.AsError(err)
		switch pe.Category {
		case provider.CatRateLimit:
			status = model.StatusRateLimited
		case provider.CatAuth:
			status = model.StatusInvalidCred
		case provider.CatTimeout:
			status = model.StatusDegraded
		default:
			status = model.StatusUnavailable
		}
		msg = pe.Message
	}
	_ = s.db.SetProviderStatus(p.ID, status, msg, store.Now())
	writeOK(w, map[string]any{
		"status": status, "message": msg, "latency_ms": time.Since(start).Milliseconds(),
	})
}

func (s *Server) adapterFor(p *model.Provider) provider.BaseProvider {
	return s.registry.Get(p.Type)
}

// providerTestPayload is the draft-test request. Nothing is written to the
// database: the operator can prove the credential works before saving.
type providerTestPayload struct {
	Type       string `json:"type"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	ProviderID string `json:"provider_id"`
}

// handleTestProviderDraft validates an unsaved provider configuration.
// When provider_id is supplied and the base URL is unchanged, the stored
// credential is reused so an edit can be tested without re-entering the key.
// A different host always requires an explicit api_key (SEC-07).
func (s *Server) handleTestProviderDraft(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req providerTestPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if !validTypes[req.Type] {
		writeErr(w, http.StatusBadRequest, "invalid_input", "unsupported provider type")
		return
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = s.registry.DefaultBaseURL(req.Type)
	}
	validated, err := provider.ValidateBaseURL(baseURL, s.allowPrivate)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unsafe_url", err.Error())
		return
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" && req.ProviderID != "" {
		stored, err := s.db.ProviderByID(req.ProviderID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "provider not found")
			return
		}
		if !strings.EqualFold(normalizeBaseURL(validated), normalizeBaseURL(stored.BaseURL)) {
			writeErr(w, http.StatusBadRequest, "api_key_required",
				"enter the API key to test a different host")
			return
		}
		apiKey, err = s.decrypt(stored.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "credential_error", "cannot decrypt credential")
			return
		}
	}
	if apiKey == "" {
		writeErr(w, http.StatusBadRequest, "api_key_required", "API key is required for a connection test")
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	status, msg := model.StatusConnected, ""
	if err := s.registry.Get(req.Type).Validate(ctx, provider.Credentials{APIKey: apiKey, BaseURL: validated}); err != nil {
		pe := provider.AsError(err)
		switch pe.Category {
		case provider.CatRateLimit:
			status = model.StatusRateLimited
		case provider.CatAuth:
			status = model.StatusInvalidCred
		case provider.CatTimeout:
			status = model.StatusDegraded
		default:
			status = model.StatusUnavailable
		}
		msg = pe.Message
	}
	writeOK(w, map[string]any{
		"status": status, "message": msg, "latency_ms": time.Since(start).Milliseconds(),
	})
}

func (s *Server) decrypt(providerID string) (string, error) {
	ct, nonce, err := s.db.CredentialFor(providerID)
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return s.box.Decrypt(ct, nonce)
}

// handleRefreshModels discovers models and upserts them.
func (s *Server) handleRefreshModels(w http.ResponseWriter, r *http.Request, _ *authSession) {
	p, err := s.db.ProviderByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if !p.Enabled {
		writeErr(w, http.StatusConflict, "provider_disabled", "provider is disabled; enable it before refreshing models")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	count, conn := s.discoverModels(ctx, p)
	if conn.Status != model.StatusConnected {
		writeErr(w, http.StatusBadGateway, "upstream_error", conn.Message)
		return
	}
	list, _ := s.db.ListModelsByProvider(p.ID)
	writeOK(w, map[string]any{"data": list, "count": count})
}

func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListModelsByProvider(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": list})
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListModels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	prov := r.URL.Query().Get("provider")
	if q != "" || prov != "" {
		filtered := make([]model.Model, 0, len(list))
		for _, m := range list {
			if prov != "" && m.ProviderID != prov {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(m.DisplayName+" "+m.ModelID+" "+m.ProviderName), q) {
				continue
			}
			filtered = append(filtered, m)
		}
		list = filtered
	}
	writeOK(w, map[string]any{"data": list})
}

var _ = fmt.Sprintf
