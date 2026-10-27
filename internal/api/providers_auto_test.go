package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/skillimport"
)

// doJSON issues an authenticated request (session cookie + CSRF) and returns
// the recorder.
func (p *providerHarness) doJSON(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session-value"})
	req.Header.Set(csrfHeader, "test-csrf")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

type providerSaveResponse struct {
	Data       *model.Provider    `json:"data"`
	Connection providerConnection `json:"connection"`
	Models     int                `json:"models"`
}

func decodeProviderSave(t *testing.T, rec *httptest.ResponseRecorder) providerSaveResponse {
	t.Helper()
	var out providerSaveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestCreateProviderAutoProbesAndImportsModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-live" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"mock-a"},{"id":"mock-b"}]}`)
	}))
	defer upstream.Close()

	h := newProviderHarness(t)
	rec := h.doJSON(t, http.MethodPost, "/admin/providers", map[string]any{
		"name": "Auto", "type": "openai-compatible", "base_url": upstream.URL + "/v1", "api_key": "sk-live",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d: %s", rec.Code, rec.Body.String())
	}
	saved := decodeProviderSave(t, rec)
	if saved.Connection.Status != model.StatusConnected {
		t.Fatalf("expected connected connection, got %+v", saved.Connection)
	}
	if saved.Models != 2 {
		t.Fatalf("expected 2 imported models, got %d", saved.Models)
	}
	if saved.Data == nil || saved.Data.Status != model.StatusConnected {
		t.Fatalf("provider status not updated: %+v", saved.Data)
	}
	models, err := h.db.ListModelsByProvider(saved.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 stored models, got %d", len(models))
	}
	for _, m := range models {
		if !m.Available {
			t.Errorf("imported model %s should be available", m.ModelID)
		}
	}
}

func TestCreateProviderKeepsProviderWhenProbeFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer upstream.Close()

	h := newProviderHarness(t)
	rec := h.doJSON(t, http.MethodPost, "/admin/providers", map[string]any{
		"name": "Broken", "type": "openai-compatible", "base_url": upstream.URL + "/v1", "api_key": "sk-wrong",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("saving a provider with a bad key should still persist it, got %d: %s", rec.Code, rec.Body.String())
	}
	saved := decodeProviderSave(t, rec)
	if saved.Connection.Status != model.StatusInvalidCred {
		t.Fatalf("expected invalid_credentials, got %+v", saved.Connection)
	}
	if saved.Models != 0 {
		t.Fatalf("expected no models, got %d", saved.Models)
	}
	if saved.Data == nil || saved.Data.Status != model.StatusInvalidCred {
		t.Fatalf("provider status not updated: %+v", saved.Data)
	}
	if saved.Connection.Message == "" {
		t.Fatal("expected an upstream error message in the connection result")
	}
}

func TestUsageModelsEndpoint(t *testing.T) {
	h := newProviderHarness(t)
	prov := h.seedProvider(t, "https://api.example/v1", false)
	priceIn, priceOut := 1.5, 6.0
	mdlID, err := h.db.UpsertModel(&model.Model{
		ProviderID: prov.ID, ModelID: "cost-model", DisplayName: "Cost Model",
		PriceIn: &priceIn, PriceOut: &priceOut, Available: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cost := 0.0015
	date := time.Now().Format("2006-01-02")
	if err := h.db.AddModelUsage(date, prov.ID, mdlID, "ok", 1000, 0, &cost); err != nil {
		t.Fatal(err)
	}

	rec := h.doJSON(t, http.MethodGet, "/admin/usage/models?days=7", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage models: got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []model.UsageModelRow `json:"data"`
		Days int                   `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Days != 7 || len(body.Data) != 1 {
		t.Fatalf("unexpected response: %+v", body)
	}
	row := body.Data[0]
	if row.ProviderID != prov.ID || row.ModelID != mdlID || row.ModelName != "Cost Model" {
		t.Fatalf("unexpected row: %+v", row)
	}
	if row.EstCost == nil || *row.EstCost != cost || row.PriceIn == nil || *row.PriceIn != priceIn {
		t.Fatalf("cost fields missing: %+v", row)
	}
}

func TestSkillImportPreviewAndInstall(t *testing.T) {
	const skillMD = "---\nname: Find Skills\ndescription: Helps find the right skill.\n---\n\n# Find Skills\n\nUse the catalog to pick a skill.\n"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/tools/git/trees/HEAD":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"tree":[{"path":"skills/find-skills/SKILL.md"}]}`)
		case "/acme/tools/HEAD/skills/find-skills/SKILL.md":
			fmt.Fprint(w, skillMD)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()

	h := newProviderHarness(t)
	h.srv.skillFetcher = &skillimport.Fetcher{Client: gh.Client(), GitHubAPI: gh.URL, RawBase: gh.URL}

	ref := "https://skills.sh/acme/tools/find-skills"
	rec := h.doJSON(t, http.MethodPost, "/admin/skills/import/preview", map[string]any{"url": ref})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: got %d: %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Data struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Content     string `json:"content"`
			SourceURL   string `json:"source_url"`
			Path        string `json:"path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Data.Name != "Find Skills" || preview.Data.Description == "" {
		t.Fatalf("unexpected preview: %+v", preview.Data)
	}
	if preview.Data.Path != "skills/find-skills/SKILL.md" {
		t.Fatalf("unexpected path: %q", preview.Data.Path)
	}

	rec = h.doJSON(t, http.MethodPost, "/admin/skills/import", map[string]any{
		"url": ref, "bind_global": false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: got %d: %s", rec.Code, rec.Body.String())
	}
	installed := decodeSkillData(t, rec)
	if installed.Type != model.SkillPrompt || !installed.Enabled {
		t.Fatalf("unexpected installed skill: %+v", installed)
	}
	if installed.Bindings.Global {
		t.Fatal("bind_global=false must not create a global binding")
	}
	if installed.Config["source_url"] != ref {
		t.Fatalf("source_url not stored: %+v", installed.Config)
	}
	content, _ := installed.Config["prompt"].(string)
	if content == "" || content == skillMD {
		t.Fatalf("expected frontmatter-stripped prompt content, got %q", content)
	}
}
