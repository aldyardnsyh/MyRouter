package gateway

import (
	"testing"
	"time"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
)

// TestRecordUsageWritesModelRollup ensures the per-model spend table used by
// the cost breakdown is written with the prices that produced the estimate.
func TestRecordUsageWritesModelRollup(t *testing.T) {
	g, db, _ := newTestGateway(t, "", "")

	route, err := db.RouteByAlias("coding")
	if err != nil {
		t.Fatal(err)
	}
	prov, err := db.ProviderByID("p1")
	if err != nil {
		t.Fatal(err)
	}
	list, err := db.ListModelsByProvider("p1")
	if err != nil || len(list) == 0 {
		t.Fatalf("models: %v", err)
	}
	mdl := &list[0]
	priceIn, priceOut := 2.0, 8.0
	if err := db.UpdateModelMeta(mdl.ID, 128000, &priceIn, &priceOut, "chat"); err != nil {
		t.Fatal(err)
	}
	mdl, _ = db.ModelByID(mdl.ID)

	key := &model.APIKey{ID: "key1", Name: "k"}
	g.recordUsage(route, prov, key, "ok", provider.Usage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500}, false, mdl)

	rows, err := db.UsageModelAggregate(time.Now().Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one usage model row, got %d", len(rows))
	}
	row := rows[0]
	if row.ModelID != mdl.ID || row.ProviderID != prov.ID {
		t.Fatalf("unexpected ids: %+v", row)
	}
	if row.InTokens != 1000 || row.OutTokens != 500 || row.Requests != 1 {
		t.Fatalf("unexpected tokens: %+v", row)
	}
	if row.EstCost == nil || *row.EstCost < 0.0059 || *row.EstCost > 0.0061 {
		t.Fatalf("expected ~0.006 cost, got %+v", row.EstCost)
	}
	if row.PriceIn == nil || *row.PriceIn != 2.0 {
		t.Fatalf("price in not joined: %+v", row.PriceIn)
	}
}
