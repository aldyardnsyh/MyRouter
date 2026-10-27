package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

// newPoolGateway creates n providers that all point at one mock upstream. When
// failEvery > 0, providers whose index is a multiple of failEvery return 402.
func newPoolGateway(t *testing.T, n, failEvery int) (*Gateway, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "pool.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := 0
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) > 0 && strings.HasPrefix(parts[0], "p") {
			fmt.Sscanf(parts[0], "p%d", &idx)
		}
		if failEvery > 0 && idx%failEvery == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(w, `{"error":{"message":"billing","type":"billing_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"1","model":"test-model","choices":[{"message":{"role":"assistant","content":"p%d"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, idx)
	}))
	t.Cleanup(srv.Close)

	box, _ := secret.New("test-key")
	registry := provider.NewRegistry(nil)
	engine := guard.New(db, &testGuardLog{})
	g := New(Options{DB: db, Registry: registry, Box: box, Guard: engine,
		Identity: guard.NewIdentity(nil), Log: logx.New(logx.LevelError), Version: "test"})

	now := store.Now()
	targets := make([]model.RouteTarget, 0, n)
	for i := 0; i < n; i++ {
		pid := fmt.Sprintf("p%d", i)
		p := &model.Provider{
			ID: pid, Name: pid, Type: "openai-compatible",
			BaseURL: fmt.Sprintf("%s/p%d", srv.URL, i), Enabled: true, CreatedAt: now,
		}
		if err := db.CreateProvider(p); err != nil {
			t.Fatalf("provider %d: %v", i, err)
		}
		ct, nonce, _ := box.Encrypt("k")
		if err := db.SaveCredential(p.ID, ct, nonce, 1, "k"); err != nil {
			t.Fatalf("credential %d: %v", i, err)
		}
		mid, err := db.UpsertModel(&model.Model{
			ProviderID: p.ID, ModelID: "test-model", DisplayName: pid, Available: true,
		})
		if err != nil {
			t.Fatalf("model %d: %v", i, err)
		}
		role := "fallback"
		if i == 0 {
			role = "primary"
		}
		targets = append(targets, model.RouteTarget{ModelID: mid, Role: role, Position: i})
	}

	route := &model.Route{
		Alias: "pool", Enabled: true, Strategy: "round_robin", Retry: 0, TimeoutMS: 2000,
		FallbackOn: "rate_limit,timeout,upstream_5xx,network,billing",
		Targets:    targets,
	}
	if err := db.SaveRoute(route); err != nil {
		t.Fatalf("route: %v", err)
	}
	key := &model.APIKey{ID: "key1", Name: "k", Prefix: "mr-sk-t", Hash: store.HashToken("test-key-secret"), Enabled: true, CreatedAt: now}
	if err := db.CreateAPIKey(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	return g, db
}

func runPoolChat(t *testing.T, g *Gateway) *Outcome {
	t.Helper()
	prep := &PreparedRequest{
		RequestID: "req-pool",
		Input:     &ChatInput{Model: "pool", Messages: []provider.ChatMessage{provider.NewTextMessage("user", "hi")}},
		APIKey:    &model.APIKey{ID: "key1", Enabled: true},
		IP:        "1.2.3.4", EstTokens: 10, Started: time.Now(),
	}
	route, err := g.db.RouteByAlias("pool")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	prep.Route = route
	out, err := g.execute(context.Background(), prep)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out
}

func TestLargePoolRoundRobin(t *testing.T) {
	const n = 100
	g, _ := newPoolGateway(t, n, 0)
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		out := runPoolChat(t, g)
		if out.Err != nil {
			t.Fatalf("request %d: %s trace=%+v", i, out.Err.Message, out.Trace)
		}
		if out.Provider == nil || out.Provider.ID == "" {
			t.Fatalf("request %d: no provider selected", i)
		}
		seen[out.Provider.ID] = true
	}
	if len(seen) != n {
		t.Fatalf("round-robin used %d/%d providers", len(seen), n)
	}
}

func TestAttemptBudgetWithManyFailures(t *testing.T) {
	const n = 50
	g, _ := newPoolGateway(t, n, 1)
	start := time.Now()
	out := runPoolChat(t, g)
	elapsed := time.Since(start)
	if out.Err == nil {
		t.Fatal("expected all targets to fail")
	}
	if len(out.Trace) > maxTargetAttempts {
		t.Fatalf("trace has %d attempts, budget is %d", len(out.Trace), maxTargetAttempts)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("budgeted failure took too long: %v", elapsed)
	}
}
