package store

import (
	"testing"

	"github.com/myrouter/myrouter/internal/model"
)

// seedProviderWithRoute creates a provider, a model, and a route pointing at it.
func seedProviderWithRoute(t *testing.T, db *DB, suffix string) (providerID, routeID string) {
	t.Helper()
	p := &model.Provider{ID: NewID("prv"), Name: "P-" + suffix, Type: "openai", BaseURL: "https://example.test/v1", CreatedAt: Now()}
	if err := db.CreateProvider(p); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	m := &model.Model{ProviderID: p.ID, ModelID: "model-" + suffix, DisplayName: "model-" + suffix, Available: true}
	id, err := db.UpsertModel(m)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	rt := &model.Route{Alias: "route-" + suffix, Enabled: true, Strategy: "priority", Retry: 1, TimeoutMS: 30000,
		Targets: []model.RouteTarget{{ModelID: id, Position: 0, Role: "primary"}}}
	if err := db.SaveRoute(rt); err != nil {
		t.Fatalf("save route: %v", err)
	}
	return p.ID, rt.ID
}

// TestDeleteProviderRemovesOrphanedRoutes pins the fix for the bug where a
// deleted provider left its routes behind with zero targets. Those routes were
// then reported as "hidden because the provider is disabled" - wrong reason and
// unrepairable state, since the provider no longer existed.
func TestDeleteProviderRemovesOrphanedRoutes(t *testing.T) {
	db, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	providerID, routeID := seedProviderWithRoute(t, db, "a")
	if _, err := db.RouteByAlias("route-a"); err != nil {
		t.Fatalf("route should exist before delete: %v", err)
	}

	removed, err := db.DeleteProvider(providerID)
	if err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 route removed, got %d", removed)
	}
	if _, err := db.RouteByID(routeID); err == nil {
		t.Fatal("route must be gone after its provider is deleted")
	}
}

// TestDeleteProviderKeepsRoutesSharedWithOtherProviders guards the opposite
// mistake: a route with another healthy target must survive.
func TestDeleteProviderKeepsRoutesSharedWithOtherProviders(t *testing.T) {
	db, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	firstID, _ := seedProviderWithRoute(t, db, "first")
	secondID, routeID := seedProviderWithRoute(t, db, "second")

	// Attach both models to one route so deleting one provider leaves a target.
	models, err := db.ListModels()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	rt, err := db.RouteByID(routeID)
	if err != nil {
		t.Fatal(err)
	}
	rt.Targets = []model.RouteTarget{
		{ModelID: ids[0], Position: 0, Role: "primary"},
		{ModelID: ids[1], Position: 1, Role: "fallback"},
	}
	if err := db.SaveRoute(rt); err != nil {
		t.Fatal(err)
	}

	if _, err := db.DeleteProvider(firstID); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	got, err := db.RouteByID(routeID)
	if err != nil {
		t.Fatalf("shared route must survive: %v", err)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("expected 1 remaining target, got %d", len(got.Targets))
	}
	_ = secondID
}

// TestListRoutesSkipsZeroTargetRows covers databases already carrying orphan
// rows from an older build: they must not be served to the UI at all.
func TestListRoutesSkipsZeroTargetRows(t *testing.T) {
	db, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Insert an orphan directly, bypassing SaveRoute's target handling.
	if _, err := db.Exec(`INSERT INTO route (id, alias, description, enabled, strategy, retry, timeout_ms, fallback_on, created_at)
		VALUES ('rt_ghost','ghost','',1,'priority',1,30000,'',?)`, Now()); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListRoutes()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.Alias == "ghost" {
			t.Fatal("route with zero targets must not be listed")
		}
	}
}

// TestMigratePrunesLegacyOrphanRoutes proves upgrade repair: an orphan row that
// exists before Open() is removed by the migration.
func TestMigratePrunesLegacyOrphanRoutes(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO route (id, alias, description, enabled, strategy, retry, timeout_ms, fallback_on, created_at)
		VALUES ('rt_legacy','legacy','',1,'priority',1,30000,'',?)`, Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	reopened, err := Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var n int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM route WHERE alias='legacy'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("migration must prune orphan routes, found %d", n)
	}
}
