package modelspec

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testAPIJSON is a tiny models.dev-shaped catalog. It intentionally reuses ids
// that exist in the embedded fallback plus two new ids so overlay/merge
// behavior is observable.
const testAPIJSON = `{
  "openai": {
    "name": "OpenAI",
    "models": {
      "gpt-4o": {
        "name": "GPT-4o",
        "limit": {"context": 128000, "output": 16384},
        "cost": {"input": 2.5, "output": 10},
        "modalities": {"input": ["text", "image"]},
        "tool_call": true,
        "attachment": true
      },
      "gpt-test-only": {
        "name": "GPT Test Only",
        "limit": {"context": 999000, "output": 32768},
        "cost": {"input": 1.5, "output": 6},
        "modalities": {"input": ["text"]},
        "tool_call": true,
        "reasoning": true,
        "attachment": false
      }
    }
  },
  "anthropic": {
    "name": "Anthropic",
    "models": {
      "claude-sonnet-4-20250514": {
        "name": "Claude Sonnet 4",
        "limit": {"context": 200000},
        "cost": {"input": 3, "output": 15},
        "modalities": {"input": ["text", "image", "pdf"]},
        "tool_call": true,
        "reasoning": true
      }
    }
  },
  "google": {
    "name": "Google",
    "models": {
      "gemini-2.5-flash": {
        "name": "Gemini 2.5 Flash",
        "limit": {"context": 1048576},
        "cost": {"input": 0.3, "output": 2.5},
        "modalities": {"input": ["text", "image", "pdf"]},
        "tool_call": true,
        "reasoning": true
      }
    }
  },
  "openrouter": {
    "name": "OpenRouter",
    "models": {
      "openrouter/auto": {
        "name": "Auto Router",
        "limit": {"context": 2000000},
        "cost": {"input": 0, "output": 0},
        "modalities": {"input": ["text", "image"]},
        "tool_call": true
      }
    }
  }
}`

func newTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertPrice(t *testing.T, label string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: price is nil, want %v", label, want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Fatalf("%s: price = %v, want %v", label, *got, want)
	}
}

func lookupOK(t *testing.T, c *Catalog, providerType, providerName, modelID string) Spec {
	t.Helper()
	spec, ok := c.Lookup(providerType, providerName, modelID)
	if !ok {
		t.Fatalf("Lookup(%q, %q, %q) = not found", providerType, providerName, modelID)
	}
	return spec
}

func TestNewLoadsEmbeddedFallback(t *testing.T) {
	c := New("")
	if got := c.Count(); got < 20 {
		t.Fatalf("Count() = %d, want at least 20 fallback models", got)
	}
	if c.UpdatedAt().IsZero() {
		t.Fatal("UpdatedAt() is zero, want the fallback curation date")
	}

	spec := lookupOK(t, c, "openai", "OpenAI", "gpt-4o")
	if spec.Context != 128000 {
		t.Fatalf("gpt-4o context = %d, want 128000", spec.Context)
	}
	assertPrice(t, "gpt-4o input", spec.PriceIn, 2.5)
	assertPrice(t, "gpt-4o output", spec.PriceOut, 10)
	if !spec.Vision || !spec.Tools || spec.Reasoning {
		t.Fatalf("gpt-4o capabilities = vision:%v tools:%v reasoning:%v", spec.Vision, spec.Tools, spec.Reasoning)
	}

	spec = lookupOK(t, c, "gemini", "Google", "gemini-2.5-pro")
	if spec.Context != 1048576 {
		t.Fatalf("gemini-2.5-pro context = %d, want 1048576", spec.Context)
	}
	assertPrice(t, "gemini-2.5-pro input", spec.PriceIn, 1.25)
	assertPrice(t, "gemini-2.5-pro output", spec.PriceOut, 10)
	if !spec.Reasoning {
		t.Fatal("gemini-2.5-pro should be marked reasoning")
	}

	if _, ok := c.Lookup("openai", "OpenAI", "definitely-not-a-model"); ok {
		t.Fatal("unknown model unexpectedly resolved")
	}
	if _, ok := c.Lookup("openai", "OpenAI", "   "); ok {
		t.Fatal("blank model id unexpectedly resolved")
	}
}

func TestLookupNormalization(t *testing.T) {
	c := New("")
	cases := []struct {
		name         string
		providerType string
		providerName string
		modelID      string
		wantContext  int
	}{
		{"exact", "openai", "OpenAI", "gpt-4o", 128000},
		{"uppercase", "openai", "OpenAI", "GPT-4O", 128000},
		{"padded", "openai", "OpenAI", "  gpt-4o  ", 128000},
		{"provider type case", "OpenAI", "OpenAI", "gpt-4o", 128000},
		{"colon tag", "openai", "OpenAI", "gpt-4o:free", 128000},
		{"vendor prefix", "openai", "OpenAI", "openai/gpt-4o", 128000},
		{"latest suffix", "openai", "OpenAI", "gpt-4o-latest", 128000},
		{"latest with prefix", "openai", "OpenAI", "openai/gpt-4o-latest", 128000},
		{"at variant", "gemini", "Google", "gemini-2.5-pro@2025-01-01", 1048576},
		{"anthropic prefixed", "anthropic", "Anthropic", "anthropic/claude-3-5-haiku-latest", 200000},
		{"compatible global index", "openai-compatible", "AcmeAI", "anthropic/claude-sonnet-4-20250514", 200000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := lookupOK(t, c, tc.providerType, tc.providerName, tc.modelID)
			if spec.Context != tc.wantContext {
				t.Fatalf("context = %d, want %d", spec.Context, tc.wantContext)
			}
		})
	}
}

func TestCompatibleProviderScoping(t *testing.T) {
	c := New("")
	// models.dev id for Groq is "groq"; the display name normalizes to it.
	spec := lookupOK(t, c, "openai-compatible", "Groq", "llama-3.3-70b-versatile")
	if spec.Context != 131072 {
		t.Fatalf("groq llama context = %d, want 131072", spec.Context)
	}
	if spec.Vision {
		t.Fatal("llama-3.3-70b-versatile should not be marked vision")
	}
	// A provider with no matching models.dev entry still hits the global index.
	spec = lookupOK(t, c, "openai-compatible", "AcmeAI", "gpt-4o")
	if spec.Context != 128000 {
		t.Fatalf("global fallback context = %d, want 128000", spec.Context)
	}
}

func TestRefreshOverHTTP(t *testing.T) {
	srv := newTestServer(t, testAPIJSON)
	dir := t.TempDir()
	c := NewWithURL(dir, srv.URL)

	before := c.Count()
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Server-only model under a known provider (exact scoped lookup).
	spec := lookupOK(t, c, "openai", "OpenAI", "gpt-test-only")
	if spec.Context != 999000 {
		t.Fatalf("gpt-test-only context = %d, want 999000", spec.Context)
	}
	assertPrice(t, "gpt-test-only input", spec.PriceIn, 1.5)
	assertPrice(t, "gpt-test-only output", spec.PriceOut, 6)
	if spec.Vision || !spec.Tools || !spec.Reasoning {
		t.Fatalf("gpt-test-only capabilities = vision:%v tools:%v reasoning:%v", spec.Vision, spec.Tools, spec.Reasoning)
	}

	// Typed aliases against the overlaid snapshot.
	if got := lookupOK(t, c, "anthropic", "Anthropic", "claude-sonnet-4-20250514").Context; got != 200000 {
		t.Fatalf("claude-sonnet-4 context = %d, want 200000", got)
	}
	gem := lookupOK(t, c, "gemini", "Google", "gemini-2.5-flash")
	if gem.Context != 1048576 {
		t.Fatalf("gemini-2.5-flash context = %d, want 1048576", gem.Context)
	}
	if !gem.Vision || !gem.Reasoning {
		t.Fatalf("gemini-2.5-flash capabilities = vision:%v reasoning:%v", gem.Vision, gem.Reasoning)
	}

	// openai-compatible provider name normalization -> models.dev provider id.
	auto := lookupOK(t, c, "openai-compatible", "OpenRouter", "openrouter/auto")
	if auto.Context != 2000000 {
		t.Fatalf("openrouter/auto context = %d, want 2000000", auto.Context)
	}
	if auto.PriceIn == nil || *auto.PriceIn != 0 {
		t.Fatalf("openrouter/auto input price = %v, want pointer to 0", auto.PriceIn)
	}

	// Merge semantics: fallback-only models survive a refresh.
	if got := lookupOK(t, c, "openai", "OpenAI", "gpt-4o-mini").Context; got != 128000 {
		t.Fatalf("fallback-only gpt-4o-mini context = %d, want 128000", got)
	}
	if got, want := c.Count(), before+2; got != want {
		t.Fatalf("Count() after refresh = %d, want %d (fallback + gpt-test-only + openrouter/auto)", got, want)
	}

	path := filepath.Join(dir, cacheFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	cache, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	for _, leaked := range []string{`"attachment"`, `"modalities"`, `"limit"`, `"tool_call"`} {
		if strings.Contains(string(cache), leaked) {
			t.Fatalf("cache leaked upstream field %s: %s", leaked, cache)
		}
	}
	for _, want := range []string{`"gpt-test-only"`, `"price_in"`, `"fetched_at"`, `"providers"`} {
		if !strings.Contains(string(cache), want) {
			t.Fatalf("cache missing %s: %s", want, cache)
		}
	}
}

func TestRefreshMemoryOnlyWithoutDataDir(t *testing.T) {
	srv := newTestServer(t, testAPIJSON)
	c := NewWithURL("", srv.URL)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	lookupOK(t, c, "openai", "OpenAI", "gpt-test-only")
}

func TestCacheRoundTripAndLoad(t *testing.T) {
	srv := newTestServer(t, testAPIJSON)
	dir := t.TempDir()
	c1 := NewWithURL(dir, srv.URL)
	ctx := context.Background()
	if err := c1.Refresh(ctx); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	// Refresh again to prove the atomic rename replaces an existing cache file.
	if err := c1.Refresh(ctx); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	wantUpdated := c1.UpdatedAt()
	wantCount := c1.Count()

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, cacheFileName))
		if err != nil {
			t.Fatalf("stat cache: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("cache mode = %o, want 0600", perm)
		}
	}

	// Load must never touch the network; point the URL somewhere unroutable.
	c2 := NewWithURL(dir, "http://127.0.0.1:9/never-fetched")
	if err := c2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c2.Count(); got != wantCount {
		t.Fatalf("Count() after Load = %d, want %d", got, wantCount)
	}
	if !c2.UpdatedAt().Equal(wantUpdated) {
		t.Fatalf("UpdatedAt() after Load = %v, want %v", c2.UpdatedAt(), wantUpdated)
	}
	spec := lookupOK(t, c2, "openai", "OpenAI", "gpt-test-only")
	if spec.Context != 999000 {
		t.Fatalf("cached gpt-test-only context = %d, want 999000", spec.Context)
	}
	lookupOK(t, c2, "openai", "OpenAI", "gpt-4o-mini") // fallback survived Load
}

func TestLoadMissingOrEmptyDataDir(t *testing.T) {
	if err := New("").Load(); err != nil {
		t.Fatalf("Load with empty dataDir: %v", err)
	}
	c := New(t.TempDir())
	if err := c.Load(); err != nil {
		t.Fatalf("Load with missing cache: %v", err)
	}
	if got := c.Count(); got < 20 {
		t.Fatalf("Count() = %d after missing-cache Load, want fallback models", got)
	}
	if _, ok := c.Lookup("openai", "OpenAI", "gpt-4o"); !ok {
		t.Fatal("fallback lookup failed after missing-cache Load")
	}
}

func TestRefreshFailureKeepsPreviousSnapshot(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, testAPIJSON)
	}))
	t.Cleanup(srv.Close)

	c := NewWithURL(t.TempDir(), srv.URL)
	ctx := context.Background()
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	wantUpdated := c.UpdatedAt()
	wantCount := c.Count()

	fail.Store(true)
	if err := c.Refresh(ctx); err == nil {
		t.Fatal("Refresh succeeded against a 500, want error")
	}
	if !c.UpdatedAt().Equal(wantUpdated) {
		t.Fatalf("UpdatedAt() changed after failed Refresh: %v != %v", c.UpdatedAt(), wantUpdated)
	}
	if got := c.Count(); got != wantCount {
		t.Fatalf("Count() changed after failed Refresh: %d != %d", got, wantCount)
	}
	lookupOK(t, c, "openai", "OpenAI", "gpt-test-only")

	bad := newTestServer(t, "this is not json")
	cBad := NewWithURL(t.TempDir(), bad.URL)
	if err := cBad.Refresh(ctx); err == nil {
		t.Fatal("Refresh accepted invalid JSON, want error")
	}
	if _, ok := cBad.Lookup("openai", "OpenAI", "gpt-4o"); !ok {
		t.Fatal("fallback lookup failed after failed Refresh")
	}
}

func TestStartRefreshesMissingCacheInBackground(t *testing.T) {
	srv := newTestServer(t, testAPIJSON)
	dir := t.TempDir()
	c := NewWithURL(dir, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := c.Lookup("openai", "OpenAI", "gpt-test-only"); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background refresh never populated the catalog")
}

func TestModelIDVariants(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"gpt-4o", []string{"gpt-4o"}},
		{"GPT-4O", []string{"gpt-4o"}},
		{"gpt-4o:free", []string{"gpt-4o:free", "gpt-4o"}},
		{"openai/gpt-4o", []string{"openai/gpt-4o", "gpt-4o"}},
		{"openai/gpt-4o-latest", []string{"openai/gpt-4o-latest", "gpt-4o-latest", "gpt-4o"}},
		{"gemini-2.5-pro@2025-01-01", []string{"gemini-2.5-pro@2025-01-01", "gemini-2.5-pro"}},
		{"anthropic/claude-3-5-haiku-latest", []string{"anthropic/claude-3-5-haiku-latest", "claude-3-5-haiku-latest", "claude-3-5-haiku"}},
		{"", nil},
		{"   ", nil},
	}
	for _, tc := range cases {
		got := modelIDVariants(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("modelIDVariants(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("modelIDVariants(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

func TestNormalizeProviderName(t *testing.T) {
	cases := map[string]string{
		"OpenRouter":       "openrouter",
		"Groq":             "groq",
		"Open AI":          "open-ai",
		"Google AI Studio": "google-ai-studio",
		"  Together.ai  ":  "together-ai",
		"":                 "",
		"---":              "",
	}
	for in, want := range cases {
		if got := normalizeProviderName(in); got != want {
			t.Fatalf("normalizeProviderName(%q) = %q, want %q", in, got, want)
		}
	}
}
