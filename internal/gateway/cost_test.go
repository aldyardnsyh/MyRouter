package gateway

import (
	"math"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
)

// ptr returns a pointer to v (helper for optional price columns).
func ptr(v float64) *float64 { return &v }

// TestCostOfPerMillionTokens pins the per-1M-token pricing formula:
// 1000 input tokens at 3.0/M and 500 output tokens at 15.0/M = 0.0105.
func TestCostOfPerMillionTokens(t *testing.T) {
	g, _, _ := newTestGateway(t, "ok", "ok")

	mdl := &model.Model{ModelID: "priced", PriceIn: ptr(3.0), PriceOut: ptr(15.0)}
	cost := g.costOf(mdl, 1000, 500)
	if cost == nil {
		t.Fatal("cost must not be nil when prices are set")
	}
	want := 3.0*1000/1e6 + 15.0*500/1e6 // 0.003 + 0.0075 = 0.0105
	if math.Abs(*cost-want) > 1e-12 {
		t.Fatalf("cost = %v, want %v", *cost, want)
	}
	if math.Abs(*cost-0.0105) > 1e-12 {
		t.Fatalf("cost = %v, want 0.0105", *cost)
	}
}

func TestCostOfNilPrices(t *testing.T) {
	g, _, _ := newTestGateway(t, "ok", "ok")

	if got := g.costOf(nil, 1000, 500); got != nil {
		t.Fatalf("nil model must yield nil cost, got %v", *got)
	}
	if got := g.costOf(&model.Model{ModelID: "free"}, 1000, 500); got != nil {
		t.Fatalf("model without prices must yield nil cost, got %v", *got)
	}
}

// TestCostOfPartialPrices covers one-sided pricing (input only / output only).
func TestCostOfPartialPrices(t *testing.T) {
	g, _, _ := newTestGateway(t, "ok", "ok")

	inOnly := g.costOf(&model.Model{PriceIn: ptr(3.0)}, 1000, 500)
	if inOnly == nil || math.Abs(*inOnly-0.003) > 1e-12 {
		t.Fatalf("input-only cost = %v, want 0.003", inOnly)
	}
	outOnly := g.costOf(&model.Model{PriceOut: ptr(15.0)}, 1000, 500)
	if outOnly == nil || math.Abs(*outOnly-0.0075) > 1e-12 {
		t.Fatalf("output-only cost = %v, want 0.0075", outOnly)
	}
}

// TestCostPersistedToUsage verifies AddUsage accumulates est_cost end to end.
func TestCostPersistedToUsage(t *testing.T) {
	g, db, _ := newTestGateway(t, "ok", "ok")
	mdl := &model.Model{PriceIn: ptr(3.0), PriceOut: ptr(15.0)}

	cost := g.costOf(mdl, 1000, 500)
	if err := db.AddUsage("2026-01-01", "rt_x", "p1", "key1", "ok", 1000, 500, false, cost); err != nil {
		t.Fatal(err)
	}
	rows, err := db.UsageAggregate("provider", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].EstCost == nil {
		t.Fatal("est_cost must be persisted")
	}
	if math.Abs(*rows[0].EstCost-0.0105) > 1e-12 {
		t.Fatalf("est_cost = %v, want 0.0105", *rows[0].EstCost)
	}
}
