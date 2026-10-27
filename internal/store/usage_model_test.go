package store

import (
	"path/filepath"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
)

func newUsageTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestUsageModelAggregate(t *testing.T) {
	db := newUsageTestDB(t)
	now := Now()
	if err := db.CreateProvider(&model.Provider{
		ID: "p1", Name: "OpenAI Prod", Type: "openai", BaseURL: "https://api.openai.com/v1",
		Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	priceIn, priceOut := 2.5, 10.0
	modelID, err := db.UpsertModel(&model.Model{
		ProviderID: "p1", ModelID: "gpt-4o", DisplayName: "GPT-4o",
		ContextWindow: 128000, PriceIn: &priceIn, PriceOut: &priceOut,
		Capabilities: "chat,vision", Available: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cost1 := 0.01
	cost2 := 0.02
	date := "2026-10-26"
	// Two priced requests and one without a price (unknown model).
	_ = db.AddModelUsage(date, "p1", modelID, "ok", 1000, 500, &cost1)
	_ = db.AddModelUsage(date, "p1", modelID, "ok", 2000, 1000, &cost2)
	_ = db.AddModelUsage(date, "p1", "mdl_unknown", "ok", 100, 50, nil)

	rows, err := db.UsageModelAggregate("2026-10-01")
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 model rows, got %d", len(rows))
	}
	first := rows[0]
	if first.ModelID != modelID {
		t.Fatalf("expected highest cost model first, got %+v", first)
	}
	if first.ProviderName != "OpenAI Prod" || first.ProviderType != "openai" || first.ModelName != "GPT-4o" {
		t.Fatalf("unexpected labels: %+v", first)
	}
	if first.Requests != 2 || first.InTokens != 3000 || first.OutTokens != 1500 {
		t.Fatalf("unexpected sums: %+v", first)
	}
	if first.EstCost == nil || *first.EstCost < 0.029 || *first.EstCost > 0.031 {
		t.Fatalf("unexpected cost: %+v", first.EstCost)
	}
	if first.PriceIn == nil || *first.PriceIn != 2.5 || first.PriceOut == nil || *first.PriceOut != 10 {
		t.Fatalf("prices not joined: %+v", first)
	}
	second := rows[1]
	if second.EstCost != nil {
		t.Fatalf("a row with no priced request must report null cost, got %v", *second.EstCost)
	}

	// Date filter: nothing from an earlier range.
	empty, err := db.UsageModelAggregate("2026-10-27")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no rows after since date, got %d", len(empty))
	}
}

func TestUpdateModelMeta(t *testing.T) {
	db := newUsageTestDB(t)
	now := Now()
	_ = db.CreateProvider(&model.Provider{ID: "p1", Name: "P", Type: "openai", BaseURL: "https://x", Enabled: true, CreatedAt: now})
	id, err := db.UpsertModel(&model.Model{ProviderID: "p1", ModelID: "m", DisplayName: "M", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	priceIn, priceOut := 1.0, 2.0
	if err := db.UpdateModelMeta(id, 200000, &priceIn, &priceOut, "chat,vision"); err != nil {
		t.Fatal(err)
	}
	got, err := db.ModelByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContextWindow != 200000 || got.PriceIn == nil || *got.PriceIn != 1.0 ||
		got.PriceOut == nil || *got.PriceOut != 2.0 || got.Capabilities != "chat,vision" {
		t.Fatalf("metadata not updated: %+v", got)
	}
	if !got.Available {
		t.Fatal("UpdateModelMeta must not change availability")
	}
}
