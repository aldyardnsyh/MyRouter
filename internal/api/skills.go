package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/skills"
	"github.com/myrouter/myrouter/internal/store"
)

// skillDTO is the API shape of a skill: config is a JSON object, not a string.
type skillDTO struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Type        string              `json:"type"`
	Enabled     bool                `json:"enabled"`
	Config      map[string]any      `json:"config"`
	ItemCount   int                 `json:"item_count"`
	CreatedAt   int64               `json:"created_at"`
	Bindings    model.SkillBindings `json:"bindings"`
	SyncAt      int64               `json:"last_sync_at"`
	SyncError   string              `json:"last_sync_error"`
	SyncItems   int                 `json:"last_sync_items"`
}

func toSkillDTO(s model.Skill) skillDTO {
	cfg := map[string]any{}
	if raw := strings.TrimSpace(s.Config); raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	routes := s.Bindings.Routes
	if routes == nil {
		routes = []string{}
	}
	return skillDTO{
		ID: s.ID, Name: s.Name, Description: s.Description, Type: s.Type,
		Enabled: s.Enabled, Config: cfg, ItemCount: s.ItemCount, CreatedAt: s.CreatedAt,
		Bindings: model.SkillBindings{Global: s.Bindings.Global, Routes: routes},
		SyncAt:   s.SyncAt, SyncError: s.SyncError, SyncItems: s.SyncItems,
	}
}

type skillCreatePayload struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Type        string         `json:"type"`
	Enabled     *bool          `json:"enabled"`
	Config      map[string]any `json:"config"`
}

type skillUpdatePayload struct {
	Name        *string        `json:"name"`
	Description *string        `json:"description"`
	Type        *string        `json:"type"`
	Enabled     *bool          `json:"enabled"`
	Config      map[string]any `json:"config"`
}

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListSkills()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]skillDTO, 0, len(list))
	for _, sk := range list {
		out = append(out, toSkillDTO(sk))
	}
	writeOK(w, map[string]any{"data": out})
}

func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request, _ *authSession) {
	sk, err := s.db.SkillByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	writeOK(w, map[string]any{"data": toSkillDTO(*sk)})
}

func (s *Server) handleCreateSkill(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req skillCreatePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "name is required")
		return
	}
	if len(name) > 64 {
		writeErr(w, http.StatusBadRequest, "invalid_input", "name must be at most 64 characters")
		return
	}
	if strings.TrimSpace(req.Type) == "" || !model.ValidSkillType(req.Type) {
		writeErr(w, http.StatusBadRequest, "invalid_input", "type must be one of: "+strings.Join(model.SkillTypes, ", "))
		return
	}
	if _, err := s.db.SkillByName(name); err == nil {
		writeErr(w, http.StatusConflict, "duplicate_name", "a skill with this name already exists")
		return
	}
	cfg, err := encodeSkillConfig(req.Config)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	sk := &model.Skill{
		ID: store.NewID("skl"), Name: name, Description: strings.TrimSpace(req.Description),
		Type: req.Type, Enabled: true, Config: cfg, CreatedAt: store.Now(),
	}
	if req.Enabled != nil {
		sk.Enabled = *req.Enabled
	}
	if err := s.db.CreateSkill(sk); err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	saved, _ := s.db.SkillByID(sk.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"data": toSkillDTO(*saved)})
}

func (s *Server) handleUpdateSkill(w http.ResponseWriter, r *http.Request, _ *authSession) {
	existing, err := s.db.SkillByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	var req skillUpdatePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name != "" && name != existing.Name {
			if len(name) > 64 {
				writeErr(w, http.StatusBadRequest, "invalid_input", "name must be at most 64 characters")
				return
			}
			if other, err := s.db.SkillByName(name); err == nil && other.ID != existing.ID {
				writeErr(w, http.StatusConflict, "duplicate_name", "a skill with this name already exists")
				return
			}
			existing.Name = name
		}
	}
	if req.Description != nil {
		existing.Description = strings.TrimSpace(*req.Description)
	}
	if req.Type != nil {
		t := strings.TrimSpace(*req.Type)
		if t != "" && t != existing.Type {
			if !model.ValidSkillType(t) {
				writeErr(w, http.StatusBadRequest, "invalid_input", "unsupported skill type")
				return
			}
			existing.Type = t
		}
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.Config != nil {
		cfg, err := encodeSkillConfig(req.Config)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		existing.Config = cfg
	}
	if err := s.db.UpdateSkill(existing); err != nil {
		writeErr(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	saved, _ := s.db.SkillByID(existing.ID)
	writeOK(w, map[string]any{"data": toSkillDTO(*saved)})
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request, _ *authSession) {
	sk, err := s.db.SkillByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	if store.IsBuiltinSkill(sk.Name) {
		writeErr(w, http.StatusForbidden, "builtin_skill", "built-in skills cannot be deleted; disable it instead")
		return
	}
	if err := s.db.DeleteSkill(sk.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

func (s *Server) handleSetSkillBindings(w http.ResponseWriter, r *http.Request, _ *authSession) {
	existing, err := s.db.SkillByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	var req struct {
		Global bool     `json:"global"`
		Routes []string `json:"routes"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	known := map[string]bool{}
	routes, err := s.db.ListRoutes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	for _, rt := range routes {
		known[strings.ToLower(rt.Alias)] = true
	}
	for _, alias := range req.Routes {
		if !known[strings.ToLower(strings.TrimSpace(alias))] {
			writeErr(w, http.StatusBadRequest, "invalid_input", "unknown route alias: "+alias)
			return
		}
	}
	if err := s.db.SetSkillBindings(existing.ID, req.Global, req.Routes); err != nil {
		writeErr(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	saved, _ := s.db.SkillByID(existing.ID)
	writeOK(w, map[string]any{"data": toSkillDTO(*saved)})
}

func (s *Server) handleSyncSkill(w http.ResponseWriter, r *http.Request, _ *authSession) {
	existing, err := s.db.SkillByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	if existing.Type != model.SkillRetrieval {
		writeErr(w, http.StatusBadRequest, "invalid_type", "only retrieval skills ingest files")
		return
	}
	report := skills.SyncSkill(s.db, *existing)
	writeOK(w, map[string]any{"data": report})
}

func (s *Server) handleListSkillItems(w http.ResponseWriter, r *http.Request, _ *authSession) {
	existing, err := s.db.SkillByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	items, total, err := s.db.SearchSkillItems(existing.ID, r.URL.Query().Get("q"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": map[string]any{"items": items, "total": total}})
}

type skillItemPayload struct {
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func (s *Server) handleCreateSkillItem(w http.ResponseWriter, r *http.Request, _ *authSession) {
	existing, err := s.db.SkillByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	var req skillItemPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "content is required")
		return
	}
	if len(content) > 512*1024 {
		writeErr(w, http.StatusBadRequest, "invalid_input", "content must be at most 512 KB")
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "note"
	}
	if kind != "note" && kind != "doc" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "kind must be note or doc")
		return
	}
	item := &model.SkillItem{
		ID: store.NewID("ski"), SkillID: existing.ID, Kind: kind,
		Source: strings.TrimSpace(req.Source), Title: strings.TrimSpace(req.Title),
		Content: content, CreatedAt: store.Now(),
	}
	if item.Source == "" {
		item.Source = "manual"
	}
	if err := s.db.CreateSkillItem(item); err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": item})
}

func (s *Server) handleDeleteSkillItem(w http.ResponseWriter, r *http.Request, _ *authSession) {
	if err := s.db.DeleteSkillItem(s.pathID(r), r.PathValue("item")); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

func (s *Server) handlePreviewSkill(w http.ResponseWriter, r *http.Request, _ *authSession) {
	existing, err := s.db.SkillByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	res := skills.Preview(s.db, *existing, r.URL.Query().Get("q"))
	if res.Applied == nil {
		res.Applied = []skills.Applied{}
	}
	writeOK(w, map[string]any{"data": res})
}

// skillImportPayload is the request for both preview and install.
type skillImportPayload struct {
	URL         string `json:"url"`
	Name        string `json:"name"`
	Description string `json:"description"`
	BindGlobal  *bool  `json:"bind_global"`
}

// handlePreviewSkillImport resolves a skills.sh link into SKILL.md content so
// the operator can confirm before installing.
func (s *Server) handlePreviewSkillImport(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req skillImportPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	url := strings.TrimSpace(req.URL)
	if url == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "url is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	imported, err := s.skillFetcher.Fetch(ctx, url)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "import_failed", err.Error())
		return
	}
	content := imported.Content
	if len(content) > 50000 {
		content = truncateUTF8Bytes(content, 50000)
	}
	writeOK(w, map[string]any{"data": map[string]any{
		"name": imported.Name, "description": imported.Description, "content": content,
		"source_url": imported.SourceURL, "owner": imported.Owner, "repo": imported.Repo,
		"path": imported.Path, "truncated": imported.Truncated || content != imported.Content,
	}})
}

// handleImportSkill installs a skills.sh skill as a prompt skill. The prompt is
// the SKILL.md body; bind_global defaults to true so the new skill applies to
// every model immediately, and the operator can narrow the binding later.
func (s *Server) handleImportSkill(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req skillImportPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	url := strings.TrimSpace(req.URL)
	if url == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "url is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	imported, err := s.skillFetcher.Fetch(ctx, url)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "import_failed", err.Error())
		return
	}

	baseName := strings.TrimSpace(req.Name)
	if baseName == "" {
		baseName = imported.Name
	}
	if baseName == "" {
		baseName = "skill"
	}
	name := s.uniqueSkillName(baseName)
	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = imported.Description
	}
	if desc == "" {
		desc = "Imported from " + imported.SourceURL
	}
	desc = skills.Truncate(desc, 500)
	content := imported.Content
	if len(content) > 48000 {
		content = truncateUTF8Bytes(content, 48000)
	}
	cfg, err := encodeSkillConfig(map[string]any{
		"prompt":     content,
		"max_chars":  8000,
		"source_url": imported.SourceURL,
		"origin":     "skills.sh",
		"repo":       imported.Owner + "/" + imported.Repo,
		"path":       imported.Path,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	sk := &model.Skill{
		ID: store.NewID("skl"), Name: name, Description: desc,
		Type: model.SkillPrompt, Enabled: true, Config: cfg, CreatedAt: store.Now(),
	}
	if err := s.db.CreateSkill(sk); err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	bindGlobal := true
	if req.BindGlobal != nil {
		bindGlobal = *req.BindGlobal
	}
	if err := s.db.SetSkillBindings(sk.ID, bindGlobal, nil); err != nil {
		writeErr(w, http.StatusInternalServerError, "binding_failed", err.Error())
		return
	}
	saved, _ := s.db.SkillByID(sk.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"data": toSkillDTO(*saved)})
}

// uniqueSkillName appends -2, -3... until the name is free (max 64 chars).
func (s *Server) uniqueSkillName(base string) string {
	base = sanitizeSkillName(base)
	for i := 1; i < 50; i++ {
		candidate := base
		if i > 1 {
			suffix := "-" + strconv.Itoa(i)
			room := 64 - len(suffix)
			if len(candidate) > room {
				candidate = candidate[:room]
			}
			candidate += suffix
		}
		if _, err := s.db.SkillByName(candidate); errors.Is(err, store.ErrNotFound) {
			return candidate
		}
	}
	return sanitizeSkillName(base)
}

// sanitizeSkillName keeps a short, printable skill name.
func sanitizeSkillName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "- ")
	if out == "" {
		out = "skill"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// truncateUTF8Bytes cuts a string to at most max bytes without splitting a
// UTF-8 rune, appending a clear truncation marker.
func truncateUTF8Bytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	b := []byte(s)[:max]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b) + "\n\n[... truncated by MyRouter: skill content too long]"
}

// encodeSkillConfig validates the config object and stores it as JSON text.
func encodeSkillConfig(cfg map[string]any) (string, error) {
	if cfg == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	if len(raw) > 64*1024 {
		return "", errConfigTooLarge
	}
	return string(raw), nil
}

var errConfigTooLarge = errors.New("config must be at most 64 KB")
