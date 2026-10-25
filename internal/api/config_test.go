package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

// configHarness is a Server on a fresh temp DB with an authenticated admin.
type configHarness struct {
	db  *store.DB
	box *secret.Box
	h   http.Handler
}

// call issues an authenticated request (session cookie + CSRF header for writes).
func (c *configHarness) call(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session-value"})
	if method != http.MethodGet {
		req.Header.Set(csrfHeader, "test-csrf")
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

// newConfigHarness builds an isolated Server with a seeded admin session.
func newConfigHarness(t *testing.T) *configHarness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	box, err := secret.New("test-master-key")
	if err != nil {
		t.Fatalf("box: %v", err)
	}

	user := &model.User{ID: store.NewID("usr"), Username: "admin", PasswordHash: "x", CreatedAt: store.Now()}
	if err := db.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(&model.Session{
		ID: store.HashToken("test-session-value"), UserID: user.ID, CSRFToken: "test-csrf",
		CreatedAt: store.Now(), ExpiresAt: store.Now() + 3600,
	}); err != nil {
		t.Fatal(err)
	}

	s := New(Deps{DB: db, Box: box, Config: ServerConfig{Version: "test"}})
	return &configHarness{db: db, box: box, h: s.Handler()}
}

// seedConfig creates one provider (with a stored credential), two models, one
// policy and one two-target route.
func seedConfig(t *testing.T, db *store.DB, box *secret.Box) {
	t.Helper()
	p := &model.Provider{ID: store.NewID("prv"), Name: "OpenAI", Type: "openai",
		BaseURL: "https://api.openai.com/v1", Status: model.StatusUnknown, Enabled: true, CreatedAt: store.Now()}
	if err := db.CreateProvider(p); err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := box.Encrypt("sk-supersecret")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCredential(p.ID, ct, nonce, box.Version(), "sk-****ret"); err != nil {
		t.Fatal(err)
	}
	m1, err := db.UpsertModel(&model.Model{ProviderID: p.ID, ModelID: "gpt-4o", DisplayName: "GPT-4o",
		ContextWindow: 128000, Capabilities: "chat,tools", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := db.UpsertModel(&model.Model{ProviderID: p.ID, ModelID: "gpt-4o-mini", DisplayName: "GPT-4o mini",
		ContextWindow: 128000, Capabilities: "chat", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	pl := &model.Policy{Name: "default", RPM: 60, TPM: 100000, OnExceed: "reject", MaxIPs: 3}
	if err := db.UpsertPolicy(pl); err != nil {
		t.Fatal(err)
	}
	route := &model.Route{Alias: "coding", Description: "coding route", Enabled: true,
		Strategy: "priority", Retry: 2, TimeoutMS: 20000, FallbackOn: "rate_limit,timeout", PolicyID: &pl.ID}
	route.Targets = []model.RouteTarget{
		{ModelID: m1, Role: "primary", Position: 0},
		{ModelID: m2, Role: "fallback", Position: 1},
	}
	if err := db.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
}

func TestConfigExportRequiresAuth(t *testing.T) {
	h := newConfigHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/config/export", nil)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated export: got %d, want 401", rec.Code)
	}
}

func TestConfigExportImportRoundTrip(t *testing.T) {
	// ---- Source instance with seeded configuration ----
	src := newConfigHarness(t)
	seedConfig(t, src.db, src.box)

	rec := src.call(t, http.MethodGet, "/admin/config/export", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: got %d: %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()

	// The export must never carry secret material.
	for _, forbidden := range []string{"ciphertext", "nonce", "sk-supersecret", "sk-****ret", "mr-sk-", "password_hash"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("export must not contain %q", forbidden)
		}
	}
	for _, field := range []string{"api_key", "hash"} {
		if strings.Contains(raw, `"`+field+`"`) {
			t.Fatalf("export must not contain the %q field", field)
		}
	}

	var exported configPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Version == "" || exported.ExportedAt == "" {
		t.Fatalf("missing metadata: %+v", exported)
	}
	if len(exported.Providers) != 1 || exported.Providers[0].Name != "OpenAI" {
		t.Fatalf("providers = %+v", exported.Providers)
	}
	if len(exported.Models) != 2 {
		t.Fatalf("models = %+v", exported.Models)
	}
	if len(exported.Policies) != 1 || exported.Policies[0].Name != "default" || exported.Policies[0].RPM != 60 {
		t.Fatalf("policies = %+v", exported.Policies)
	}
	if len(exported.Routes) != 1 {
		t.Fatalf("routes = %+v", exported.Routes)
	}
	r0 := exported.Routes[0]
	if r0.Alias != "coding" || r0.PrimaryModel != "OpenAI/gpt-4o" {
		t.Fatalf("route = %+v", r0)
	}
	if len(r0.FallbackModels) != 1 || r0.FallbackModels[0] != "OpenAI/gpt-4o-mini" {
		t.Fatalf("fallback_models = %+v", r0.FallbackModels)
	}
	if len(r0.FallbackOn) != 2 {
		t.Fatalf("fallback_on = %+v", r0.FallbackOn)
	}

	// ---- Import into a FRESH database ----
	fresh := newConfigHarness(t)
	rec2 := fresh.call(t, http.MethodPost, "/admin/config/import", exported)
	if rec2.Code != http.StatusOK {
		t.Fatalf("import: got %d: %s", rec2.Code, rec2.Body.String())
	}
	var res struct {
		Providers int            `json:"providers"`
		Models    int            `json:"models"`
		Policies  int            `json:"policies"`
		Routes    int            `json:"routes"`
		Updated   map[string]int `json:"updated"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Providers != 1 || res.Models != 2 || res.Policies != 1 || res.Routes != 1 {
		t.Fatalf("import counts = %+v", res)
	}
	if res.Updated["providers"] != 0 || res.Updated["policies"] != 0 || res.Updated["routes"] != 0 {
		t.Fatalf("fresh import must not report updates: %+v", res.Updated)
	}

	// Providers present, without credentials (credential is skipped on import).
	provs, err := fresh.db.ListProviders()
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 1 || provs[0].Name != "OpenAI" {
		t.Fatalf("providers = %+v", provs)
	}
	if _, _, err := fresh.db.CredentialFor(provs[0].ID); err == nil {
		t.Fatal("imported provider must not carry a credential")
	}

	// Route resolves to the right model chain and keeps the policy binding.
	rt, err := fresh.db.RouteByAlias("coding")
	if err != nil {
		t.Fatalf("imported route missing: %v", err)
	}
	if rt.Retry != 2 || rt.TimeoutMS != 20000 || !rt.Enabled {
		t.Fatalf("route fields = %+v", rt)
	}
	if len(rt.Targets) != 2 || rt.Targets[0].Role != "primary" || rt.Targets[0].ModelName != "gpt-4o" {
		t.Fatalf("targets = %+v", rt.Targets)
	}
	fbs := rt.Fallbacks()
	if len(fbs) != 1 || fbs[0].ModelName != "gpt-4o-mini" {
		t.Fatalf("fallbacks = %+v", fbs)
	}

	// Policies present.
	pls, err := fresh.db.ListPolicies()
	if err != nil {
		t.Fatal(err)
	}
	if len(pls) != 1 || pls[0].Name != "default" || pls[0].RPM != 60 || pls[0].MaxIPs != 3 {
		t.Fatalf("policies = %+v", pls)
	}

	// ---- Re-import updates existing rows and reports it ----
	rec3 := fresh.call(t, http.MethodPost, "/admin/config/import", exported)
	if rec3.Code != http.StatusOK {
		t.Fatalf("re-import: got %d: %s", rec3.Code, rec3.Body.String())
	}
	var res2 struct {
		Updated map[string]int `json:"updated"`
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	if res2.Updated["providers"] != 1 || res2.Updated["policies"] != 1 || res2.Updated["routes"] != 1 {
		t.Fatalf("re-import must report updates: %+v", res2.Updated)
	}

	// Still exactly one of each after the upsert.
	if provs, _ := fresh.db.ListProviders(); len(provs) != 1 {
		t.Fatalf("provider duplicated on re-import: %d", len(provs))
	}
	if routes, _ := fresh.db.ListRoutes(); len(routes) != 1 {
		t.Fatalf("route duplicated on re-import: %d", len(routes))
	}
}

func TestConfigImportValidation(t *testing.T) {
	h := newConfigHarness(t)

	cases := []struct {
		name string
		body configPayload
	}{
		{"unknown provider for model", configPayload{
			Providers: []configProvider{{Name: "A", Type: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true}},
			Models:    []configModel{{ProviderName: "B", ModelID: "x"}},
		}},
		{"unsupported provider type", configPayload{
			Providers: []configProvider{{Name: "A", Type: "not-a-type", BaseURL: "https://api.openai.com/v1", Enabled: true}},
		}},
		{"provider name missing", configPayload{
			Providers: []configProvider{{Name: "  ", Type: "openai", BaseURL: "https://api.openai.com/v1"}},
		}},
		{"route references unknown model", configPayload{
			Providers: []configProvider{{Name: "A", Type: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true}},
			Routes:    []configRoute{{Alias: "x", PrimaryModel: "A/missing"}},
		}},
		{"route alias invalid", configPayload{
			Providers: []configProvider{{Name: "A", Type: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true}},
			Models:    []configModel{{ProviderName: "A", ModelID: "m"}},
			Routes:    []configRoute{{Alias: "bad alias!", PrimaryModel: "A/m"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.call(t, http.MethodPost, "/admin/config/import", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestConfigImportModelIDsWithSlashes covers provider-prefixed references to
// model ids that themselves contain a slash (common for OSS models).
func TestConfigImportModelIDsWithSlashes(t *testing.T) {
	h := newConfigHarness(t)

	payload := configPayload{
		Providers: []configProvider{{Name: "Groq", Type: "openai-compatible", BaseURL: "https://api.groq.com/openai/v1", Enabled: true}},
		Models: []configModel{
			{ProviderName: "Groq", ModelID: "meta-llama/Llama-3.1-8B", DisplayName: "Llama 3.1 8B"},
			{ProviderName: "Groq", ModelID: "qwen/qwen3-32b", DisplayName: "Qwen3 32B"},
		},
		Routes: []configRoute{{
			Alias: "oss", Enabled: true, Retry: 1, TimeoutMS: 15000,
			FallbackOn: []string{"rate_limit"}, PrimaryModel: "Groq/meta-llama/Llama-3.1-8B",
			FallbackModels: []string{"Groq/qwen/qwen3-32b"},
		}},
	}
	rec := h.call(t, http.MethodPost, "/admin/config/import", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: got %d: %s", rec.Code, rec.Body.String())
	}
	rt, err := h.db.RouteByAlias("oss")
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.Targets) != 2 {
		t.Fatalf("targets = %+v", rt.Targets)
	}
	if rt.Targets[0].ModelName != "meta-llama/Llama-3.1-8B" {
		t.Fatalf("primary = %q", rt.Targets[0].ModelName)
	}
	if rt.Fallbacks()[0].ModelName != "qwen/qwen3-32b" {
		t.Fatalf("fallback = %q", rt.Fallbacks()[0].ModelName)
	}

	// Round-trip: the export of that route must reference the same models.
	rec2 := h.call(t, http.MethodGet, "/admin/config/export", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("export: %d", rec2.Code)
	}
	var out configPayload
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Routes[0].PrimaryModel != "Groq/meta-llama/Llama-3.1-8B" {
		t.Fatalf("exported primary = %q", out.Routes[0].PrimaryModel)
	}
}
