package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// authedDo issues an authenticated admin request with a known session cookie and
// CSRF token so the skills handlers can be exercised end to end.
func (a *authHarness) authedDo(t *testing.T, method, path, sessionValue, csrf string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
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

// skillsHarness returns a harness with an admin session and its credentials.
func skillsHarness(t *testing.T) (*authHarness, string, string) {
	t.Helper()
	a := newAuthHarness(t, true)
	user, err := a.db.UserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	const (
		sessionValue = "skills-session-cookie"
		csrfToken    = "skills-csrf-token"
	)
	now := store.Now()
	if err := a.db.CreateSession(&model.Session{
		ID: store.HashToken(sessionValue), UserID: user.ID, CSRFToken: csrfToken,
		CreatedAt: now, ExpiresAt: now + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	return a, sessionValue, csrfToken
}

func decodeSkillData(t *testing.T, rec *httptest.ResponseRecorder) skillDTO {
	t.Helper()
	var body struct {
		Data skillDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body.Data
}

func TestSkillsCRUDBindingsAndPreview(t *testing.T) {
	a, session, csrf := skillsHarness(t)

	// Create.
	rec := a.authedDo(t, http.MethodPost, "/admin/skills", session, csrf, map[string]any{
		"name": "docs", "description": "project docs", "type": "retrieval",
		"config": map[string]any{"top_k": 3, "sync_glob": "*.md"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d: %s", rec.Code, rec.Body.String())
	}
	created := decodeSkillData(t, rec)
	if created.ID == "" || created.Name != "docs" || created.Config["top_k"] != float64(3) {
		t.Fatalf("unexpected created skill: %+v", created)
	}
	if !created.Enabled {
		t.Error("new skills should be enabled by default")
	}

	// Duplicate name conflicts.
	rec = a.authedDo(t, http.MethodPost, "/admin/skills", session, csrf, map[string]any{
		"name": "docs", "type": "prompt",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate name: got %d, want 409", rec.Code)
	}

	// Bind globally.
	rec = a.authedDo(t, http.MethodPut, "/admin/skills/"+created.ID+"/bindings", session, csrf, map[string]any{
		"global": true, "routes": []string{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("bindings: got %d: %s", rec.Code, rec.Body.String())
	}
	if bound := decodeSkillData(t, rec); !bound.Bindings.Global {
		t.Fatalf("global binding not saved: %+v", bound.Bindings)
	}

	// Unknown route alias is rejected.
	rec = a.authedDo(t, http.MethodPut, "/admin/skills/"+created.ID+"/bindings", session, csrf, map[string]any{
		"global": false, "routes": []string{"does-not-exist"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown route: got %d, want 400", rec.Code)
	}

	// Add a knowledge item and search it.
	rec = a.authedDo(t, http.MethodPost, "/admin/skills/"+created.ID+"/items", session, csrf, map[string]any{
		"title": "Deploy", "content": "Run the single binary behind Caddy.", "source": "readme",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add item: got %d: %s", rec.Code, rec.Body.String())
	}
	rec = a.authedDo(t, http.MethodGet, "/admin/skills/"+created.ID+"/items?q=deploy", session, csrf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list items: got %d: %s", rec.Code, rec.Body.String())
	}
	var itemsBody struct {
		Data struct {
			Items []model.SkillItem `json:"items"`
			Total int               `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &itemsBody); err != nil {
		t.Fatal(err)
	}
	if itemsBody.Data.Total != 1 || len(itemsBody.Data.Items) != 1 {
		t.Fatalf("expected one matching item, got %+v", itemsBody.Data)
	}
	// Unfiltered list includes 2 retrieval starter docs + the manual item.
	rec = a.authedDo(t, http.MethodGet, "/admin/skills/"+created.ID+"/items", session, csrf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list all items: got %d: %s", rec.Code, rec.Body.String())
	}
	var allBody struct {
		Data struct {
			Items []model.SkillItem `json:"items"`
			Total int               `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &allBody); err != nil {
		t.Fatal(err)
	}
	if allBody.Data.Total != 3 {
		t.Fatalf("expected 2 starter + 1 manual item, got %+v", allBody.Data)
	}

	// Preview shows the injected context and the applied skill list.
	rec = a.authedDo(t, http.MethodGet, "/admin/skills/"+created.ID+"/preview?q=deploy+caddy", session, csrf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "single binary behind Caddy") {
		t.Fatalf("preview missing knowledge: %s", rec.Body.String())
	}

	// Sync on a skill with default sync_paths reports no matching files.
	rec = a.authedDo(t, http.MethodPost, "/admin/skills/"+created.ID+"/sync", session, csrf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no files matched") {
		t.Fatalf("unexpected sync report: %s", rec.Body.String())
	}

	// Update description + disable.
	disabled := false
	rec = a.authedDo(t, http.MethodPatch, "/admin/skills/"+created.ID, session, csrf, map[string]any{
		"description": "changed", "enabled": disabled,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d: %s", rec.Code, rec.Body.String())
	}
	updated := decodeSkillData(t, rec)
	if updated.Description != "changed" || updated.Enabled {
		t.Fatalf("update not applied: %+v", updated)
	}

	// Delete.
	rec = a.authedDo(t, http.MethodDelete, "/admin/skills/"+created.ID, session, csrf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d: %s", rec.Code, rec.Body.String())
	}
	rec = a.authedDo(t, http.MethodGet, "/admin/skills/"+created.ID, session, csrf, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted skill should be gone, got %d", rec.Code)
	}
}

func TestBuiltinSkillCannotBeDeleted(t *testing.T) {
	a, session, csrf := skillsHarness(t)
	if err := a.db.SeedBuiltinSkills(); err != nil {
		t.Fatal(err)
	}
	list, err := a.db.ListSkills()
	if err != nil || len(list) == 0 {
		t.Fatalf("skills: %v", err)
	}
	var target model.Skill
	for _, sk := range list {
		if sk.Name == "xerj" {
			target = sk
		}
	}
	if target.ID == "" {
		t.Fatal("xerj builtin not seeded")
	}
	rec := a.authedDo(t, http.MethodDelete, "/admin/skills/"+target.ID, session, csrf, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("builtin delete: got %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestSkillsRequireAuthAndCSRF(t *testing.T) {
	a, session, _ := skillsHarness(t)
	rec := a.authedDo(t, http.MethodGet, "/admin/skills", "", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: got %d, want 401", rec.Code)
	}
	rec = a.authedDo(t, http.MethodPost, "/admin/skills", session, "", map[string]any{
		"name": "x", "type": "prompt",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf: got %d, want 403", rec.Code)
	}
}
