package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/myrouter/myrouter/internal/model"
)

// ActivityFilter limits an activity query.
type ActivityFilter struct {
	RouteAlias string
	ProviderID string
	APIKeyID   string
	Status     string
	Since      int64
	Search     string
	Limit      int
	Offset     int
}

const activitySelect = `SELECT l.id, COALESCE(l.request_id,''), l.ts, COALESCE(l.route_id,''), COALESCE(r.alias,''),
	COALESCE(l.requested_model,''), COALESCE(l.model_id,''), COALESCE(m.model_id,''), COALESCE(l.provider_id,''), COALESCE(p.name,''),
	COALESCE(l.api_key_id,''), COALESCE(k.name,''), COALESCE(k.prefix,''), COALESCE(l.ip,''), COALESCE(l.device_id,''),
	l.status, l.http_status, l.latency_ms, l.in_tokens, l.out_tokens, COALESCE(l.fallback_from,''), COALESCE(l.error_code,''),
	COALESCE(l.guard_reason,''), COALESCE(l.trace,'[]')
	FROM request_log l
	LEFT JOIN route r ON r.id = l.route_id
	LEFT JOIN model m ON m.id = l.model_id
	LEFT JOIN provider p ON p.id = l.provider_id
	LEFT JOIN api_key k ON k.id = l.api_key_id`

func (db *DB) queryActivity(where string, args ...any) ([]model.RequestLog, error) {
	rows, err := db.Query(activitySelect+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.RequestLog{}
	for rows.Next() {
		r, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanActivity(rows *sql.Rows) (model.RequestLog, error) {
	var r model.RequestLog
	var trace string
	err := rows.Scan(&r.ID, &r.RequestID, &r.TS, &r.RouteID, &r.RouteAlias, &r.RequestedModel, &r.ModelID, &r.ModelName,
		&r.ProviderID, &r.ProviderName, &r.APIKeyID, &r.APIKeyName, &r.APIKeyPrefix, &r.IP, &r.DeviceID,
		&r.Status, &r.HTTPStatus, &r.LatencyMS, &r.InTokens, &r.OutTokens, &r.FallbackFrom, &r.ErrorCode, &r.GuardReason, &trace)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(trace), &r.Trace)
	if r.Trace == nil {
		r.Trace = []model.TraceStep{}
	}
	return r, nil
}

// InsertRequestLog stores one activity record.
func (db *DB) InsertRequestLog(r *model.RequestLog) error {
	if r.ID == "" {
		r.ID = NewID("req")
	}
	trace, _ := json.Marshal(r.Trace)
	_, err := db.Exec(`INSERT INTO request_log (id, request_id, ts, route_id, requested_model, model_id, provider_id, api_key_id, ip, device_id,
		status, http_status, latency_ms, in_tokens, out_tokens, fallback_from, error_code, guard_reason, trace)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.RequestID, r.TS, nullStr(r.RouteID), r.RequestedModel, nullStr(r.ModelID), nullStr(r.ProviderID), nullStr(r.APIKeyID),
		r.IP, nullStr(r.DeviceID), r.Status, r.HTTPStatus, r.LatencyMS, r.InTokens, r.OutTokens, r.FallbackFrom, r.ErrorCode, r.GuardReason, string(trace))
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ListActivity returns filtered activity rows plus total count.
func (db *DB) ListActivity(f ActivityFilter) ([]model.RequestLog, int, error) {
	var where []string
	var args []any
	if f.RouteAlias != "" {
		where = append(where, "r.alias = ?")
		args = append(args, f.RouteAlias)
	}
	if f.ProviderID != "" {
		where = append(where, "l.provider_id = ?")
		args = append(args, f.ProviderID)
	}
	if f.APIKeyID != "" {
		where = append(where, "l.api_key_id = ?")
		args = append(args, f.APIKeyID)
	}
	if f.Status != "" {
		where = append(where, "l.status = ?")
		args = append(args, f.Status)
	}
	if f.Since > 0 {
		where = append(where, "l.ts >= ?")
		args = append(args, f.Since)
	}
	if f.Search != "" {
		where = append(where, "(LOWER(l.request_id) LIKE ? OR l.ip LIKE ? OR LOWER(COALESCE(k.name,'')) LIKE ?)")
		like := "%" + strings.ToLower(f.Search) + "%"
		args = append(args, like, "%"+f.Search+"%", like)
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM request_log l LEFT JOIN route r ON r.id=l.route_id LEFT JOIN api_key k ON k.id=l.api_key_id `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	where = append(where, "1=1")
	clause = "WHERE " + strings.Join(where, " AND ")
	q := clause + " ORDER BY l.ts DESC LIMIT ? OFFSET ?"
	list, err := db.queryActivity(q, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// GetActivity loads one activity row including trace.
func (db *DB) GetActivity(id string) (*model.RequestLog, error) {
	rows, err := db.queryActivity("WHERE l.id = ? OR l.request_id = ? ORDER BY l.ts DESC LIMIT 1", id, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return &rows[0], nil
}

// MetricSet is the dashboard/activity summary.
type MetricSet struct {
	Requests     int64    `json:"requests"`
	OK           int64    `json:"ok"`
	Failed       int64    `json:"failed"`
	Blocked      int64    `json:"blocked"`
	Fallbacks    int64    `json:"fallbacks"`
	InTokens     int64    `json:"in_tokens"`
	OutTokens    int64    `json:"out_tokens"`
	AvgLatency   float64  `json:"avg_latency_ms"`
	SuccessRate  float64  `json:"success_rate"`
	Providers    int64    `json:"providers"`
	ActiveRoutes int64    `json:"active_routes"`
	EstCost      *float64 `json:"est_cost"`
}

// Metrics computes aggregate metrics since a timestamp.
func (db *DB) Metrics(since int64) (*MetricSet, error) {
	m := &MetricSet{}
	var avg sql.NullFloat64
	err := db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status IN ('error') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='blocked' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN fallback_from <> '' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(in_tokens),0), COALESCE(SUM(out_tokens),0), AVG(latency_ms)
		FROM request_log WHERE ts >= ?`, since).
		Scan(&m.Requests, &m.OK, &m.Failed, &m.Blocked, &m.Fallbacks, &m.InTokens, &m.OutTokens, &avg)
	if err != nil {
		return nil, err
	}
	if avg.Valid {
		m.AvgLatency = avg.Float64
	}
	if m.Requests > 0 {
		m.SuccessRate = float64(m.OK) / float64(m.Requests) * 100
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider WHERE enabled=1`).Scan(&m.Providers); err != nil {
		return nil, err
	}
	// A route only counts as active when at least one target can serve, which
	// requires both an available model and an enabled provider. Otherwise a
	// disabled provider would leave stale aliases counted as live.
	if err := db.QueryRow(`SELECT COUNT(*) FROM route r WHERE r.enabled=1 AND EXISTS (
		SELECT 1 FROM route_target t
		JOIN model m ON m.id = t.model_id
		JOIN provider p ON p.id = m.provider_id
		WHERE t.route_id = r.id AND m.available=1 AND p.enabled=1
	)`).Scan(&m.ActiveRoutes); err != nil {
		return nil, err
	}
	return m, nil
}

// RequestsSeries returns request counts bucketed per hour for the last n hours.
func (db *DB) RequestsSeries(hours int) ([]int64, error) {
	now := time.Now()
	buckets := make([]int64, hours)
	start := now.Add(-time.Duration(hours-1) * time.Hour).Truncate(time.Hour)
	rows, err := db.Query(`SELECT ts FROM request_log WHERE ts >= ?`, start.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			return nil, err
		}
		idx := int(time.Unix(ts, 0).Truncate(time.Hour).Sub(start).Hours())
		if idx >= 0 && idx < hours {
			buckets[idx]++
		}
	}
	return buckets, rows.Err()
}

// SeriesPoint is one bucket of the dashboard chart. Every field is real data
// from request_log so the chart, the tooltip and the summary agree.
type SeriesPoint struct {
	Start      int64   `json:"start"`
	Requests   int64   `json:"requests"`
	OK         int64   `json:"ok"`
	Failed     int64   `json:"failed"`
	Blocked    int64   `json:"blocked"`
	Fallbacks  int64   `json:"fallbacks"`
	InTokens   int64   `json:"in_tokens"`
	OutTokens  int64   `json:"out_tokens"`
	AvgLatency float64 `json:"avg_latency_ms"`
	MaxLatency int64   `json:"max_latency_ms"`
}

// ChartSeries buckets request_log into n buckets of bucketDur seconds ending
// at the current bucket. Hourly buckets answer "when is traffic heaviest" for
// the last day; daily buckets answer "which day was busiest" for 3d and 7d.
func (db *DB) ChartSeries(buckets int, bucketDur time.Duration) ([]SeriesPoint, error) {
	if buckets <= 0 {
		return []SeriesPoint{}, nil
	}
	now := time.Now()
	step := int64(bucketDur.Seconds())
	if step <= 0 {
		step = 3600
	}
	// Align the first bucket to the bucket boundary so labels are stable.
	current := now.Unix() - now.Unix()%step
	start := current - int64(buckets-1)*step

	out := make([]SeriesPoint, buckets)
	for i := range out {
		out[i].Start = start + int64(i)*step
	}

	rows, err := db.Query(`SELECT ts, status, COALESCE(in_tokens,0), COALESCE(out_tokens,0),
		COALESCE(latency_ms,0), CASE WHEN COALESCE(fallback_from,'') <> '' THEN 1 ELSE 0 END
		FROM request_log WHERE ts >= ?`, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// latency sums are accumulated per bucket, then averaged once at the end.
	latSum := make([]int64, buckets)
	for rows.Next() {
		var (
			ts, inTok, outTok, lat, fb int64
			status                     string
		)
		if err := rows.Scan(&ts, &status, &inTok, &outTok, &lat, &fb); err != nil {
			return nil, err
		}
		idx := int((ts - start) / step)
		if idx < 0 || idx >= buckets {
			continue
		}
		p := &out[idx]
		p.Requests++
		p.InTokens += inTok
		p.OutTokens += outTok
		p.Fallbacks += fb
		switch status {
		case "ok":
			p.OK++
		case "blocked":
			p.Blocked++
		default:
			p.Failed++
		}
		latSum[idx] += lat
		if lat > p.MaxLatency {
			p.MaxLatency = lat
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Requests > 0 {
			out[i].AvgLatency = float64(latSum[i]) / float64(out[i].Requests)
		}
	}
	return out, nil
}

// AddUsage upserts usage_daily for a request.
func (db *DB) AddUsage(date, routeID, providerID, apiKeyID, status string, inTokens, outTokens int, fallback bool, cost *float64) error {
	ok, failed := 0, 0
	switch status {
	case "ok":
		ok = 1
	case "error":
		failed = 1
	}
	fb := 0
	if fallback {
		fb = 1
	}
	_, err := db.Exec(`INSERT INTO usage_daily (date, route_id, provider_id, api_key_id, requests, ok, failed, fallbacks, in_tokens, out_tokens, est_cost)
		VALUES (?,?,?,?,1,?,?,?,?,?,?)
		ON CONFLICT(date, route_id, provider_id, api_key_id) DO UPDATE SET
			requests = requests + 1, ok = ok + excluded.ok, failed = failed + excluded.failed,
			fallbacks = fallbacks + excluded.fallbacks, in_tokens = in_tokens + excluded.in_tokens,
			out_tokens = out_tokens + excluded.out_tokens,
			est_cost = CASE WHEN est_cost IS NULL AND excluded.est_cost IS NULL THEN NULL
			                ELSE COALESCE(est_cost,0) + COALESCE(excluded.est_cost,0) END`,
		date, routeID, providerID, apiKeyID, ok, failed, fb, inTokens, outTokens, cost)
	return err
}

// AddModelUsage upserts usage_model_daily for one successful request.
func (db *DB) AddModelUsage(date, providerID, modelID, status string, inTokens, outTokens int, cost *float64) error {
	ok, failed := 0, 0
	switch status {
	case "ok":
		ok = 1
	case "error":
		failed = 1
	}
	_, err := db.Exec(`INSERT INTO usage_model_daily (date, provider_id, model_id, requests, ok, failed, in_tokens, out_tokens, est_cost)
		VALUES (?,?,?,1,?,?,?,?,?)
		ON CONFLICT(date, provider_id, model_id) DO UPDATE SET
			requests = requests + 1, ok = ok + excluded.ok, failed = failed + excluded.failed,
			in_tokens = in_tokens + excluded.in_tokens, out_tokens = out_tokens + excluded.out_tokens,
			est_cost = CASE WHEN est_cost IS NULL AND excluded.est_cost IS NULL THEN NULL
			                ELSE COALESCE(est_cost,0) + COALESCE(excluded.est_cost,0) END`,
		date, providerID, modelID, ok, failed, inTokens, outTokens, cost)
	return err
}

// UsageModelAggregate returns spend per provider+model since a date.
func (db *DB) UsageModelAggregate(since string) ([]model.UsageModelRow, error) {
	rows, err := db.Query(`SELECT
		u.provider_id, COALESCE(p.name,'Unknown'), COALESCE(p.type,''),
		u.model_id, COALESCE(NULLIF(m.display_name,''), NULLIF(m.model_id,''), u.model_id),
		COALESCE(SUM(u.requests),0), COALESCE(SUM(u.ok),0), COALESCE(SUM(u.failed),0),
		COALESCE(SUM(u.in_tokens),0), COALESCE(SUM(u.out_tokens),0),
		CASE WHEN SUM(CASE WHEN u.est_cost IS NOT NULL THEN 1 ELSE 0 END) = 0 THEN NULL ELSE SUM(COALESCE(u.est_cost,0)) END,
		m.price_in, m.price_out
		FROM usage_model_daily u
		LEFT JOIN provider p ON p.id = u.provider_id
		LEFT JOIN model m ON m.id = u.model_id
		WHERE u.date >= ?
		GROUP BY u.provider_id, u.model_id
		ORDER BY COALESCE(SUM(u.est_cost),0) DESC, SUM(u.in_tokens + u.out_tokens) DESC
		LIMIT 200`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.UsageModelRow{}
	for rows.Next() {
		var r model.UsageModelRow
		if err := rows.Scan(&r.ProviderID, &r.ProviderName, &r.ProviderType,
			&r.ModelID, &r.ModelName, &r.Requests, &r.OK, &r.Failed,
			&r.InTokens, &r.OutTokens, &r.EstCost, &r.PriceIn, &r.PriceOut); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageAggregate groups usage_daily by a dimension: route|provider|key|day.
func (db *DB) UsageAggregate(groupBy string, since string) ([]model.UsageRow, error) {
	var keyCol, labelExpr, join, group string
	switch groupBy {
	case "provider":
		keyCol, labelExpr = "u.provider_id", "COALESCE(p.name,'Unknown')"
		join = "LEFT JOIN provider p ON p.id = u.provider_id"
		group = "u.provider_id"
	case "key":
		keyCol, labelExpr = "u.api_key_id", "COALESCE(k.name,'Unknown')"
		join = "LEFT JOIN api_key k ON k.id = u.api_key_id"
		group = "u.api_key_id"
	case "day":
		keyCol, labelExpr = "u.date", "u.date"
		group = "u.date"
	default:
		keyCol, labelExpr = "u.route_id", "COALESCE(r.alias,'Unknown')"
		join = "LEFT JOIN route r ON r.id = u.route_id"
		group = "u.route_id"
	}
	q := fmt.Sprintf(`SELECT %s, %s,
		COALESCE(SUM(u.requests),0), COALESCE(SUM(u.ok),0), COALESCE(SUM(u.failed),0), COALESCE(SUM(u.fallbacks),0),
		COALESCE(SUM(u.in_tokens),0), COALESCE(SUM(u.out_tokens),0),
		CASE WHEN SUM(CASE WHEN u.est_cost IS NOT NULL THEN 1 ELSE 0 END) = 0 THEN NULL ELSE SUM(COALESCE(u.est_cost,0)) END
		FROM usage_daily u %s WHERE u.date >= ? GROUP BY %s ORDER BY SUM(u.requests) DESC LIMIT 100`,
		keyCol, labelExpr, join, group)
	rows, err := db.Query(q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.UsageRow{}
	for rows.Next() {
		var r model.UsageRow
		if err := rows.Scan(&r.Key, &r.Label, &r.Requests, &r.OK, &r.Failed, &r.Fallbacks, &r.InTokens, &r.OutTokens, &r.EstCost); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PurgeRequestLogs deletes activity older than ts.
func (db *DB) PurgeRequestLogs(ts int64) (int64, error) {
	res, err := db.Exec(`DELETE FROM request_log WHERE ts < ?`, ts)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ProviderHealth returns the provider list used by dashboards.
func (db *DB) ProviderHealth() ([]model.Provider, error) { return db.ListProviders() }

var _ = errors.Is
