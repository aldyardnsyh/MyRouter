package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

// newTestGateway builds a full gateway with two mock providers.
func newTestGateway(t *testing.T, primaryMode, backupMode string) (*Gateway, *store.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mkSrv := func(mode string, content string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"data":[{"id":"test-model"}]}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch mode {
			case "402":
				w.WriteHeader(http.StatusPaymentRequired)
				fmt.Fprint(w, `{"error":{"message":"billing","type":"billing_error"}}`)
			case "429":
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"message":"limited"}}`)
			case "401":
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			case "500":
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"boom"}}`)
			default:
				fmt.Fprintf(w, `{"id":"1","model":"test-model","choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`, content)
			}
		}))
	}
	primarySrv := mkSrv(primaryMode, "from primary")
	backupSrv := mkSrv(backupMode, "from backup")
	t.Cleanup(primarySrv.Close)
	t.Cleanup(backupSrv.Close)

	box, _ := secret.New("test-key")
	registry := provider.NewRegistry(nil)
	engine := guard.New(db, &testGuardLog{})

	g := New(Options{DB: db, Registry: registry, Box: box, Guard: engine,
		Identity: guard.NewIdentity(nil), Log: logx.New(logx.LevelError), Version: "test"})

	now := store.Now()
	p1 := &model.Provider{ID: "p1", Name: "Primary", Type: "openai-compatible", BaseURL: primarySrv.URL, Enabled: true, CreatedAt: now}
	p2 := &model.Provider{ID: "p2", Name: "Backup", Type: "openai-compatible", BaseURL: backupSrv.URL, Enabled: true, CreatedAt: now}
	if err := db.CreateProvider(p1); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProvider(p2); err != nil {
		t.Fatal(err)
	}
	ct, n, _ := box.Encrypt("k1")
	_ = db.SaveCredential(p1.ID, ct, n, 1, "k1")
	ct, n, _ = box.Encrypt("k2")
	_ = db.SaveCredential(p2.ID, ct, n, 1, "k2")

	m1, _ := db.UpsertModel(&model.Model{ProviderID: p1.ID, ModelID: "test-model", DisplayName: "T1", Available: true})
	m2, _ := db.UpsertModel(&model.Model{ProviderID: p2.ID, ModelID: "test-model", DisplayName: "T2", Available: true})

	route := &model.Route{Alias: "coding", Enabled: true, Strategy: "priority", Retry: 0, TimeoutMS: 5000,
		FallbackOn: "rate_limit,timeout,upstream_5xx,network,billing"}
	route.Targets = []model.RouteTarget{{ModelID: m1, Role: "primary"}, {ModelID: m2, Role: "fallback"}}
	if err := db.SaveRoute(route); err != nil {
		t.Fatal(err)
	}

	key := &model.APIKey{ID: "key1", Name: "k", Prefix: "mr-sk-t", Hash: store.HashToken("test-key-secret"), Enabled: true, CreatedAt: now}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatal(err)
	}
	return g, db, "test-key-secret"
}

type testGuardLog struct{}

func (testGuardLog) Warn(msg string, f ...guard.Field)  {}
func (testGuardLog) Debug(msg string, f ...guard.Field) {}
func (testGuardLog) Info(msg string, f ...guard.Field)  {}

func runChat(t *testing.T, g *Gateway, keySecret string) *Outcome {
	t.Helper()
	prep := &PreparedRequest{
		RequestID: "req1",
		Input:     &ChatInput{Model: "coding", Messages: []provider.ChatMessage{provider.NewTextMessage("user", "hi")}},
		Route:     &model.Route{ID: "", Alias: "coding"},
		APIKey:    &model.APIKey{ID: "key1", Enabled: true},
		IP:        "1.2.3.4", EstTokens: 10, Started: time.Now(),
	}
	route, err := g.db.RouteByAlias("coding")
	if err != nil {
		t.Fatal(err)
	}
	prep.Route = route
	out, err := g.execute(context.Background(), prep)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out
}

func prep_now() time.Time { return time.Now() }

func TestFallbackOn429(t *testing.T) {
	g, _, _ := newTestGateway(t, "429", "ok")
	out := runChat(t, g, "test-key-secret")
	if out.Err != nil {
		t.Fatalf("expected fallback success, got error: %s trace=%+v routeFB=%s", out.Err.Message, out.Trace, "x")
	}
	if out.Response.Content != "from backup" {
		t.Fatalf("content = %q, want 'from backup'", out.Response.Content)
	}
	if !out.Fallback {
		t.Fatal("Fallback flag must be true")
	}
	if len(out.Trace) != 2 || out.Trace[0].Outcome != "error" || out.Trace[1].Outcome != "success" {
		t.Fatalf("trace = %+v", out.Trace)
	}
}

func TestFallbackOn500(t *testing.T) {
	g, _, _ := newTestGateway(t, "500", "ok")
	out := runChat(t, g, "test-key-secret")
	if out.Err != nil {
		t.Fatalf("expected fallback success: %s", out.Err.Message)
	}
	if out.Response.Content != "from backup" {
		t.Fatalf("content = %q", out.Response.Content)
	}
}

func TestNoFallbackOnAuthError(t *testing.T) {
	// Primary returns 401 (not fallback eligible) -> error surfaces immediately.
	g, _, _ := newTestGateway(t, "401", "ok")
	out := runChat(t, g, "test-key-secret")
	if out.Err == nil {
		t.Fatal("expected auth error to surface")
	}
	if len(out.Trace) != 1 {
		t.Fatalf("auth error must not attempt fallback, trace = %+v", out.Trace)
	}
}

func TestPrimarySuccessSkipsBackup(t *testing.T) {
	g, _, _ := newTestGateway(t, "ok", "ok")
	out := runChat(t, g, "test-key-secret")
	if out.Err != nil || out.Response.Content != "from primary" {
		t.Fatalf("content = %v err = %v", out.Response, out.Err)
	}
	if out.Fallback {
		t.Fatal("Fallback must be false on primary success")
	}
	_ = json.Marshal
	_ = os.Getenv
}

// TestStreamingOutlivesAttemptContext is a regression test for the streaming
// cancellation bug: execute() used to cancel the per-attempt context as soon
// as StreamChat returned, which aborted the upstream body before any chunk
// reached the client.
func TestStreamingOutlivesAttemptContext(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "stream.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"stream-model"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n")
		if fl != nil {
			fl.Flush()
		}
		time.Sleep(80 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"stream\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(upstream.Close)

	box, _ := secret.New("test-key")
	registry := provider.NewRegistry(nil)
	engine := guard.New(db, &testGuardLog{})
	g := New(Options{DB: db, Registry: registry, Box: box, Guard: engine,
		Identity: guard.NewIdentity(nil), Log: logx.New(logx.LevelError), Version: "test"})

	now := store.Now()
	p := &model.Provider{ID: "ps", Name: "Stream", Type: "openai-compatible", BaseURL: upstream.URL, Enabled: true, CreatedAt: now}
	if err := db.CreateProvider(p); err != nil {
		t.Fatal(err)
	}
	ct, n, _ := box.Encrypt("k")
	_ = db.SaveCredential(p.ID, ct, n, 1, "k")
	mid, _ := db.UpsertModel(&model.Model{ProviderID: p.ID, ModelID: "stream-model", DisplayName: "S", Available: true})
	route := &model.Route{Alias: "streaming", Enabled: true, Strategy: "priority", Retry: 0, TimeoutMS: 5000}
	route.Targets = []model.RouteTarget{{ModelID: mid, Role: "primary"}}
	if err := db.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	key := &model.APIKey{ID: "ks", Name: "k", Prefix: "mr-sk-s", Hash: store.HashToken("stream-secret"), Enabled: true, CreatedAt: now}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatal(err)
	}

	prep := &PreparedRequest{
		RequestID: "req-stream",
		Input:     &ChatInput{Model: "streaming", Stream: true, Messages: []provider.ChatMessage{provider.NewTextMessage("user", "hi")}},
		Route:     route,
		APIKey:    &model.APIKey{ID: "ks", Enabled: true},
		IP:        "1.2.3.4", EstTokens: 10, Started: time.Now(),
	}
	out, err := g.execute(context.Background(), prep)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.Err != nil {
		t.Fatalf("unexpected error: %s", out.Err.Message)
	}
	if out.Stream == nil {
		t.Fatal("expected a stream")
	}
	var content string
	for ev := range out.Stream {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		content += ev.Delta
	}
	if content != "hello stream" {
		t.Fatalf("content = %q, want %q", content, "hello stream")
	}
}

func TestPlaygroundNonStream(t *testing.T) {
	g, _, _ := newTestGateway(t, "ok", "ok")
	res, err := g.Playground(context.Background(), PlaygroundRequest{
		Route:     "coding",
		Messages:  []provider.ChatMessage{provider.NewTextMessage("user", "ping")},
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("playground: %v", err)
	}
	if res.Status != "ok" || res.Content != "from primary" {
		t.Fatalf("status=%s content=%q err=%s", res.Status, res.Content, res.Error)
	}
	if res.Provider != "Primary" || res.Model != "test-model" {
		t.Fatalf("provider=%s model=%s", res.Provider, res.Model)
	}
	if len(res.Trace) == 0 || res.RequestID == "" {
		t.Fatalf("missing trace/request id: %+v", res)
	}
}

func TestPlaygroundFallsBack(t *testing.T) {
	g, _, _ := newTestGateway(t, "429", "ok")
	res, err := g.Playground(context.Background(), PlaygroundRequest{
		Route:    "coding",
		Messages: []provider.ChatMessage{provider.NewTextMessage("user", "ping")},
	})
	if err != nil {
		t.Fatalf("playground: %v", err)
	}
	if res.Status != "ok" || res.Content != "from backup" || !res.Fallback {
		t.Fatalf("status=%s content=%q fallback=%v", res.Status, res.Content, res.Fallback)
	}
}

func TestPlaygroundUnknownRoute(t *testing.T) {
	g, _, _ := newTestGateway(t, "ok", "ok")
	if _, err := g.Playground(context.Background(), PlaygroundRequest{
		Route: "nope", Messages: []provider.ChatMessage{provider.NewTextMessage("user", "x")},
	}); err == nil {
		t.Fatal("expected an error for an unknown route")
	}
}

func TestFallbackOnBilling402(t *testing.T) {
	g, _, _ := newTestGateway(t, "402", "ok")
	out := runChat(t, g, "test-key-secret")
	if out.Err != nil {
		t.Fatalf("expected billing fallback success, got: %s trace=%+v", out.Err.Message, out.Trace)
	}
	if out.Response.Content != "from backup" || !out.Fallback {
		t.Fatalf("content=%q fallback=%v", out.Response.Content, out.Fallback)
	}
	if len(out.Trace) != 2 || out.Trace[0].Category != "billing" || out.Trace[1].Outcome != "success" {
		t.Fatalf("trace = %+v", out.Trace)
	}
}

func TestBreakerSkipsCoolingProvider(t *testing.T) {
	g, _, _ := newTestGateway(t, "402", "ok")
	first := runChat(t, g, "test-key-secret")
	if first.Err != nil || first.Response.Content != "from backup" {
		t.Fatalf("first call: err=%v content=%q", first.Err, first.Response.Content)
	}
	second := runChat(t, g, "test-key-secret")
	if second.Err != nil || second.Response.Content != "from backup" {
		t.Fatalf("second call: err=%v content=%q", second.Err, second.Response.Content)
	}
	if len(second.Trace) != 2 || second.Trace[0].Outcome != "skipped" || second.Trace[0].Category != "cooldown" {
		t.Fatalf("expected primary to be skipped while cooling down, trace=%+v", second.Trace)
	}
}

func TestRoundRobinRotatesAccounts(t *testing.T) {
	g, db, _ := newTestGateway(t, "ok", "ok")
	route, err := db.RouteByAlias("coding")
	if err != nil {
		t.Fatal(err)
	}
	route.Strategy = "round_robin"
	if err := db.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	first := runChat(t, g, "test-key-secret")
	second := runChat(t, g, "test-key-secret")
	if first.Response == nil || first.Response.Content != "from primary" {
		t.Fatalf("first call content=%v err=%v", first.Response, first.Err)
	}
	if second.Response == nil || second.Response.Content != "from backup" {
		t.Fatalf("second call content=%v err=%v", second.Response, second.Err)
	}
}
