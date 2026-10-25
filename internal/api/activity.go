package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/myrouter/myrouter/internal/guard"
	"github.com/myrouter/myrouter/internal/store"
)

func parseTimeRange(v string) int64 {
	now := time.Now()
	switch v {
	case "1h":
		return now.Add(-time.Hour).Unix()
	case "24h", "":
		return now.Add(-24 * time.Hour).Unix()
	case "7d":
		return now.Add(-7 * 24 * time.Hour).Unix()
	case "30d":
		return now.Add(-30 * 24 * time.Hour).Unix()
	}
	return now.Add(-24 * time.Hour).Unix()
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func (s *Server) handleListActivity(w http.ResponseWriter, r *http.Request, _ *authSession) {
	q := r.URL.Query()
	f := store.ActivityFilter{
		RouteAlias: q.Get("route"),
		ProviderID: q.Get("provider"),
		APIKeyID:   q.Get("key"),
		Status:     q.Get("status"),
		Search:     strings.TrimSpace(q.Get("q")),
		Since:      parseTimeRange(q.Get("range")),
		Limit:      atoiDefault(q.Get("limit"), 25),
		Offset:     atoiDefault(q.Get("offset"), 0),
	}
	list, total, err := s.db.ListActivity(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": list, "total": total, "limit": f.Limit, "offset": f.Offset})
}

func (s *Server) handleGetActivity(w http.ResponseWriter, r *http.Request, _ *authSession) {
	entry, err := s.db.GetActivity(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "activity not found")
		return
	}
	writeOK(w, map[string]any{"data": entry})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request, _ *authSession) {
	q := r.URL.Query()
	groupBy := q.Get("group_by")
	switch groupBy {
	case "provider", "key", "day", "route":
	default:
		groupBy = "route"
	}
	days := atoiDefault(q.Get("days"), 7)
	if days <= 0 || days > 90 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	rows, err := s.db.UsageAggregate(groupBy, since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	m, err := s.db.Metrics(time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": rows, "summary": m, "group_by": groupBy, "days": days})
}

// seriesRange maps a dashboard range to bucket count and width. Hourly for a
// single day keeps the intraday shape; daily for 3d and 7d keeps the buckets
// wide enough to read on a card.
func seriesRange(v string) (int, time.Duration, string) {
	switch v {
	case "3d":
		return 3, 24 * time.Hour, "3d"
	case "7d":
		return 7, 24 * time.Hour, "7d"
	case "30d":
		return 30, 24 * time.Hour, "30d"
	default:
		return 24, time.Hour, "24h"
	}
}

// handleSeries returns the dashboard chart series for a range, with the same
// aggregates the chart summary shows (requests, ok/failed/blocked, tokens,
// latency) so the tooltip and the summary never disagree.
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request, _ *authSession) {
	buckets, width, label := seriesRange(r.URL.Query().Get("range"))
	series, err := s.db.ChartSeries(buckets, width)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	var sum store.SeriesPoint
	for _, p := range series {
		sum.Requests += p.Requests
		sum.OK += p.OK
		sum.Failed += p.Failed
		sum.Blocked += p.Blocked
		sum.Fallbacks += p.Fallbacks
		sum.InTokens += p.InTokens
		sum.OutTokens += p.OutTokens
		if p.MaxLatency > sum.MaxLatency {
			sum.MaxLatency = p.MaxLatency
		}
	}
	if sum.Requests > 0 {
		var latSum float64
		for _, p := range series {
			latSum += p.AvgLatency * float64(p.Requests)
		}
		sum.AvgLatency = latSum / float64(sum.Requests)
	}
	writeOK(w, map[string]any{
		"data":       series,
		"summary":    sum,
		"range":      label,
		"bucket_sec": int(width.Seconds()),
	})
}

// handleOverview powers the dashboard.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request, _ *authSession) {
	metrics24h, err := s.db.Metrics(time.Now().Add(-24 * time.Hour).Unix())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	series, _ := s.db.RequestsSeries(24)
	recent, _, _ := s.db.ListActivity(store.ActivityFilter{Limit: 5})
	providers, _ := s.db.ListProviders()
	settings, _ := s.db.AllSettings()

	baseURL := settings["base_url"]
	port := settings["api_port"]
	if baseURL == "" {
		host := r.Host
		if port != "" && !strings.Contains(host, ":") {
			host = host + ":" + port
		}
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		baseURL = scheme + "://" + host + "/v1"
	}
	if metrics24h.Providers == 0 {
		metrics24h.Providers = int64(len(providers))
	}
	writeOK(w, map[string]any{
		"metrics":   metrics24h,
		"series":    series,
		"recent":    recent,
		"providers": providers,
		"base_url":  baseURL,
		"version":   s.version,
	})
}

// handleHealth reports server health and the trusted proxy state. It is an
// authenticated endpoint: the payload exposes internal topology (provider
// count, degraded providers, number of trusted proxies) and must stay behind
// the admin session. Unauthenticated liveness is served by /healthz.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request, _ *authSession) {
	providers, _ := s.db.ListProviders()
	degraded := 0
	for _, p := range providers {
		if p.Status == "degraded" || p.Status == "unavailable" || p.Status == "invalid_credentials" {
			degraded++
		}
	}
	status := "ok"
	if degraded > 0 {
		status = "degraded"
	}
	trusted := guard.TrustedProxiesFromSettings(s.db)
	writeOK(w, map[string]any{
		"status": status, "version": s.version, "providers": len(providers),
		"degraded": degraded, "trusted_proxies": len(trusted),
	})
}
