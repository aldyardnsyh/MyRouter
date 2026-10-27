package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/store"
)

// The Activity view reads three separate columns: the model that served the
// request, the Guard scope that blocked it, and the alias a fallback moved away
// from. They used to share one argument, which left model_id NULL for every
// row, wrote the route alias into guard_reason on success, and never set
// fallback_from. These tests pin the three columns independently.

func doChat(t *testing.T, g *Gateway, secret string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"coding","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	return rec
}

func latestLog(t *testing.T, g *Gateway) (modelID, guardReason, fallbackFrom string, status string) {
	t.Helper()
	list, _, err := g.db.ListActivity(store.ActivityFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list activity: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("no activity row was written")
	}
	row := list[0]
	return row.ModelID, row.GuardReason, row.FallbackFrom, row.Status
}

func TestActivityRecordsServedModel(t *testing.T) {
	g, db, secret := newTestGateway(t, "ok", "ok")
	if rec := doChat(t, g, secret); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	modelID, guardReason, fallbackFrom, status := latestLog(t, g)
	if status != "ok" {
		t.Fatalf("status = %q, want ok", status)
	}
	if modelID == "" {
		t.Fatal("model_id must name the model that served the request")
	}
	// The id must resolve, otherwise usage_model_daily cannot be attributed.
	if _, err := db.ModelByID(modelID); err != nil {
		t.Fatalf("model_id %q does not resolve: %v", modelID, err)
	}
	if guardReason != "" {
		t.Fatalf("guard_reason = %q, want empty on a successful request", guardReason)
	}
	if fallbackFrom != "" {
		t.Fatalf("fallback_from = %q, want empty when the primary answered", fallbackFrom)
	}
}

func TestActivityRecordsFallbackFromAlias(t *testing.T) {
	g, _, secret := newTestGateway(t, "429", "ok")
	if rec := doChat(t, g, secret); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	modelID, guardReason, fallbackFrom, status := latestLog(t, g)
	if status != "ok" {
		t.Fatalf("status = %q, want ok", status)
	}
	if modelID == "" {
		t.Fatal("model_id must name the fallback model that served the request")
	}
	// fallback_from holds the alias the client asked for, not the Guard scope.
	if fallbackFrom != "coding" {
		t.Fatalf("fallback_from = %q, want the requested alias", fallbackFrom)
	}
	if guardReason != "" {
		t.Fatalf("guard_reason = %q, want empty: a fallback is not a Guard block", guardReason)
	}
}

func TestActivityRecordsGuardReasonWhenBlocked(t *testing.T) {
	g, _, _ := newTestGateway(t, "ok", "ok")
	// An unknown alias is rejected before routing, so no model is involved.
	if rec := doChat(t, g, "wrong-secret"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	modelID, _, fallbackFrom, status := latestLog(t, g)
	if status != "blocked" {
		t.Fatalf("status = %q, want blocked", status)
	}
	if modelID != "" {
		t.Fatalf("model_id = %q, want empty for a pre-routing rejection", modelID)
	}
	if fallbackFrom != "" {
		t.Fatalf("fallback_from = %q, want empty for a rejected request", fallbackFrom)
	}
}

// A successful request must appear in the daily rollups exactly once, with a
// cost when the model has prices. Writing usage in both the log path and the
// record path doubled the counters.
func TestUsageCountedOncePerRequest(t *testing.T) {
	g, db, secret := newTestGateway(t, "ok", "ok")
	if rec := doChat(t, g, secret); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	rows, err := db.UsageAggregate("route", "0000-00-00")
	if err != nil {
		t.Fatalf("usage aggregate: %v", err)
	}
	var total, ok int64
	for _, r := range rows {
		total += r.Requests
		ok += r.OK
	}
	if total != 1 || ok != 1 {
		t.Fatalf("usage counted %d requests (%d ok), want exactly 1: %+v", total, ok, rows)
	}
}

// Failures must still reach the Usage rollup: the dashboard and the Usage tab
// count them, and dropping them silently hides outages.
func TestUsageCountsBlockedRequests(t *testing.T) {
	g, _, _ := newTestGateway(t, "ok", "ok")
	// An unknown alias is rejected after the key is authenticated but before a
	// route exists, so this asserts on the request_log row only.
	if rec := doChat(t, g, "wrong-secret"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if _, _, _, status := latestLog(t, g); status != "blocked" {
		t.Fatalf("status = %q, want blocked", status)
	}

	// A route-level block (key not allowed to use the alias) does have a route,
	// so it must appear in usage_daily as a failure.
	g2, db2, secret2 := newTestGateway(t, "ok", "ok")
	key, err := db2.APIKeyByHash(store.HashToken(secret2))
	if err != nil {
		t.Fatalf("key lookup: %v", err)
	}
	key.AllowedRoutes = "other-alias"
	if err := db2.UpdateAPIKey(key); err != nil {
		t.Fatalf("update key: %v", err)
	}
	if rec := doChat(t, g2, secret2); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	rows, err := db2.UsageAggregate("route", "0000-00-00")
	if err != nil {
		t.Fatalf("usage aggregate: %v", err)
	}
	var total int64
	for _, r := range rows {
		total += r.Requests
	}
	if total != 1 {
		t.Fatalf("blocked request counted %d times, want 1: %+v", total, rows)
	}
	_ = g
}
