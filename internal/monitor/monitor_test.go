package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

// newFixture builds an isolated DB + vault + registry for a single test.
func newFixture(t *testing.T, handler http.HandlerFunc) (*store.DB, *provider.Registry, *secret.Box, *httptest.Server) {
	t.Helper()

	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	box, err := secret.New("test-master-key-for-monitor")
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	registry := provider.NewRegistry(provider.GuardedClient(true, 0))
	return db, registry, box, srv
}

// seedProvider inserts an enabled openai-compatible provider with a stored
// credential pointing at baseURL.
func seedProvider(t *testing.T, db *store.DB, box *secret.Box, name, baseURL, apiKey string, enabled bool) *model.Provider {
	t.Helper()

	p := &model.Provider{
		ID: store.NewID("prv"), Name: name, Type: model.ProviderCompatible,
		BaseURL: baseURL, Status: model.StatusUnknown, Enabled: enabled, CreatedAt: store.Now(),
	}
	if err := db.CreateProvider(p); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if apiKey != "" {
		ct, nonce, err := box.Encrypt(apiKey)
		if err != nil {
			t.Fatalf("encrypt credential: %v", err)
		}
		if err := db.SaveCredential(p.ID, ct, nonce, box.Version(), secret.Mask(apiKey)); err != nil {
			t.Fatalf("save credential: %v", err)
		}
	}
	return p
}

func statusOf(t *testing.T, db *store.DB, id string) model.Provider {
	t.Helper()

	p, err := db.ProviderByID(id)
	if err != nil {
		t.Fatalf("reload provider: %v", err)
	}
	return *p
}

// TestRunOnceMarksProviderConnected is the happy path: the mock upstream
// answers /models with 200, so the provider flips to "connected".
func TestRunOnceMarksProviderConnected(t *testing.T) {
	var gotPath, gotAuth string
	db, registry, box, srv := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"mock-model"}]}`))
	})

	p := seedProvider(t, db, box, "Mock", srv.URL, "sk-mock-key-123", true)
	m := New(db, registry, box, time.Minute, logx.New(logx.LevelError))
	m.RunOnce(context.Background())

	if gotPath != "/models" {
		t.Fatalf("validate path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer sk-mock-key-123" {
		t.Fatalf("authorization header = %q, want bearer credential", gotAuth)
	}
	got := statusOf(t, db, p.ID)
	if got.Status != model.StatusConnected {
		t.Fatalf("status = %q, want %q (last_error=%q)", got.Status, model.StatusConnected, got.LastError)
	}
	if got.LastError != "" {
		t.Fatalf("last_error = %q, want empty", got.LastError)
	}
	if got.LastCheck == nil {
		t.Fatal("last_check must be set after a probe")
	}
}

// failingAdapter is a BaseProvider whose Validate always returns a preset
// normalized error, letting the test assert the monitor's status taxonomy
// independently of any real adapter's error handling.
type failingAdapter struct {
	typeName string
	err      error
}

func (a *failingAdapter) Type() string                                         { return a.typeName }
func (a *failingAdapter) DefaultBaseURL() string                               { return "http://upstream.invalid/v1" }
func (a *failingAdapter) Validate(context.Context, provider.Credentials) error { return a.err }
func (a *failingAdapter) ListModels(context.Context, provider.Credentials) ([]provider.ModelInfo, error) {
	return nil, a.err
}
func (a *failingAdapter) Chat(context.Context, provider.Credentials, string, provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, a.err
}
func (a *failingAdapter) StreamChat(context.Context, provider.Credentials, string, provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	return nil, a.err
}
func (a *failingAdapter) MapError(int, []byte) *provider.Error { return provider.AsError(a.err) }

// TestRunOnceMapsUpstreamFailures checks the status taxonomy mapping.
func TestRunOnceMapsUpstreamFailures(t *testing.T) {
	cases := []struct {
		name     string
		category provider.ErrorCategory
		want     string
	}{
		{"rate limited", provider.CatRateLimit, model.StatusRateLimited},
		{"bad credential", provider.CatAuth, model.StatusInvalidCred},
		{"upstream 5xx", provider.CatUpstream5xx, model.StatusUnavailable},
		{"network error", provider.CatNetwork, model.StatusUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _, box, srv := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})

			adapter := &failingAdapter{
				typeName: "mock-" + string(tc.category),
				err:      &provider.Error{Category: tc.category, Message: "upstream said no"},
			}
			registry := provider.NewRegistry(provider.GuardedClient(true, 0))
			registry.Register(adapter)

			p := &model.Provider{
				ID: store.NewID("prv"), Name: "Mock", Type: adapter.typeName, BaseURL: srv.URL,
				Status: model.StatusUnknown, Enabled: true, CreatedAt: store.Now(),
			}
			if err := db.CreateProvider(p); err != nil {
				t.Fatalf("create provider: %v", err)
			}
			ct, nonce, err := box.Encrypt("sk-mock-key-123")
			if err != nil {
				t.Fatalf("encrypt credential: %v", err)
			}
			if err := db.SaveCredential(p.ID, ct, nonce, box.Version(), "sk-****-123"); err != nil {
				t.Fatalf("save credential: %v", err)
			}

			m := New(db, registry, box, time.Minute, logx.New(logx.LevelError))
			m.RunOnce(context.Background())

			got := statusOf(t, db, p.ID)
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
			if got.LastError == "" {
				t.Fatal("last_error must explain the failure")
			}
		})
	}
}

// TestRunOnceMapsTimeoutToDegraded verifies a deadline breach reports degraded.
func TestRunOnceMapsTimeoutToDegraded(t *testing.T) {
	release := make(chan struct{})
	db, registry, box, srv := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })

	p := seedProvider(t, db, box, "Slow", srv.URL, "sk-mock-key-123", true)
	m := New(db, registry, box, time.Minute, logx.New(logx.LevelError))

	// The monitor's own 10s probe timeout is too slow for a unit test, so the
	// parent context carries the short deadline instead.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	m.RunOnce(ctx)

	got := statusOf(t, db, p.ID)
	if got.Status != model.StatusDegraded {
		t.Fatalf("status = %q, want %q (last_error=%q)", got.Status, model.StatusDegraded, got.LastError)
	}
}

// TestRunOnceSkipsDisabledAndMissingCredentials covers the two guard rails.
func TestRunOnceSkipsDisabledAndMissingCredentials(t *testing.T) {
	calls := 0
	db, registry, box, srv := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	disabled := seedProvider(t, db, box, "Disabled", srv.URL, "sk-mock-key-123", false)
	noKey := seedProvider(t, db, box, "NoKey", srv.URL, "", true)

	m := New(db, registry, box, time.Minute, logx.New(logx.LevelError))
	m.RunOnce(context.Background())

	if calls != 0 {
		t.Fatalf("upstream called %d times, want 0", calls)
	}
	if got := statusOf(t, db, disabled.ID); got.Status != model.StatusUnknown {
		t.Fatalf("disabled provider status = %q, want untouched %q", got.Status, model.StatusUnknown)
	}
	if got := statusOf(t, db, noKey.ID); got.Status != model.StatusInvalidCred {
		t.Fatalf("keyless provider status = %q, want %q", got.Status, model.StatusInvalidCred)
	}
}

// TestSyncClientRebuildsOnSettingChange proves the SSRF setting is re-read each
// cycle and that a change swaps the adapter registry's HTTP client.
func TestSyncClientRebuildsOnSettingChange(t *testing.T) {
	db, registry, box, srv := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	seedProvider(t, db, box, "Mock", srv.URL, "sk-mock-key-123", true)

	m := New(db, registry, box, time.Minute, logx.New(logx.LevelError))
	before := m.registry
	m.RunOnce(context.Background())
	if m.registry != before {
		t.Fatal("registry must not be rebuilt when the setting is unchanged")
	}
	if m.allowPrivate == nil || *m.allowPrivate {
		t.Fatal("allow_private_upstreams should default to false")
	}

	if err := db.SetSetting("allow_private_upstreams", "true"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	m.RunOnce(context.Background())
	if m.registry == before {
		t.Fatal("registry must be rebuilt after allow_private_upstreams changes")
	}
	if m.allowPrivate == nil || !*m.allowPrivate {
		t.Fatal("allow_private_upstreams change was not picked up")
	}

	// The rebuilt registry must still be able to probe the mock upstream.
	p, err := db.ListProviders()
	if err != nil || len(p) != 1 {
		t.Fatalf("list providers: %v (n=%d)", err, len(p))
	}
	if got := statusOf(t, db, p[0].ID); got.Status != model.StatusConnected {
		t.Fatalf("status after rebuild = %q, want %q", got.Status, model.StatusConnected)
	}
}

// TestNewDefaultsInterval documents the 5-minute default cadence.
func TestNewDefaultsInterval(t *testing.T) {
	db, registry, box, _ := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	if got := New(db, registry, box, 0, nil).interval; got != DefaultInterval {
		t.Fatalf("interval = %v, want %v", got, DefaultInterval)
	}
	if got := New(db, registry, box, -time.Second, nil).interval; got != DefaultInterval {
		t.Fatalf("interval = %v, want %v", got, DefaultInterval)
	}
	if got := New(db, registry, box, time.Minute, nil).interval; got != time.Minute {
		t.Fatalf("interval = %v, want 1m", got)
	}
}
