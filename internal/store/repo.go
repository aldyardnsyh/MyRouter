package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/myrouter/myrouter/internal/model"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ---------------------------------------------------------------------------
// Users & sessions
// ---------------------------------------------------------------------------

// CreateUser inserts the admin user.
func (db *DB) CreateUser(u *model.User) error {
	_, err := db.Exec(`INSERT INTO user (id, username, password_hash, created_at) VALUES (?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, u.CreatedAt)
	return err
}

// CreateFirstUser inserts the bootstrap admin only when the user table is still
// empty. The existence check and the insert are a single statement so two
// concurrent setup requests cannot both win (no TOCTOU). It reports whether the
// row was created; false means setup was already completed by someone else.
func (db *DB) CreateFirstUser(u *model.User) (bool, error) {
	res, err := db.Exec(`INSERT INTO user (id, username, password_hash, created_at)
		SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM user)`,
		u.ID, u.Username, u.PasswordHash, u.CreatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// UserByUsername loads a user by username.
func (db *DB) UserByUsername(username string) (*model.User, error) {
	var u model.User
	err := db.QueryRow(`SELECT id, username, password_hash, created_at FROM user WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// UserByID loads a user by id.
func (db *DB) UserByID(id string) (*model.User, error) {
	var u model.User
	err := db.QueryRow(`SELECT id, username, password_hash, created_at FROM user WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// UserCount returns the number of admin accounts.
func (db *DB) UserCount() (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM user`).Scan(&n)
	return n, err
}

// CreateSession stores a session (id is the hash of the cookie value).
func (db *DB) CreateSession(s *model.Session) error {
	_, err := db.Exec(`INSERT INTO session (id, user_id, csrf_token, created_at, expires_at) VALUES (?,?,?,?,?)`,
		s.ID, s.UserID, s.CSRFToken, s.CreatedAt, s.ExpiresAt)
	return err
}

// SessionByID loads a valid (unexpired) session.
func (db *DB) SessionByID(id string, now int64) (*model.Session, error) {
	var s model.Session
	err := db.QueryRow(`SELECT id, user_id, csrf_token, created_at, expires_at FROM session WHERE id = ? AND expires_at > ?`, id, now).
		Scan(&s.ID, &s.UserID, &s.CSRFToken, &s.CreatedAt, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

// DeleteSession removes a session.
func (db *DB) DeleteSession(id string) error {
	_, err := db.Exec(`DELETE FROM session WHERE id = ?`, id)
	return err
}

// RevokeOtherSessions deletes every session of userID except keepSessionID and
// reports how many rows were removed. The error is returned unmodified so the
// caller can decide that a failed revocation is a hard failure (SEC-05): a
// password change must never be reported as successful while stolen cookies
// remain usable.
func (db *DB) RevokeOtherSessions(userID, keepSessionID string) (int64, error) {
	res, err := db.Exec(`DELETE FROM session WHERE user_id = ? AND id <> ?`, userID, keepSessionID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// PurgeSessions deletes expired sessions.
func (db *DB) PurgeSessions(now int64) error {
	_, err := db.Exec(`DELETE FROM session WHERE expires_at <= ?`, now)
	return err
}

// DeleteAllSessions removes every admin session and reports how many rows were
// deleted. Startup uses it so a restart always requires a fresh login: a cookie
// left over from the previous run must not walk straight into the console.
func (db *DB) DeleteAllSessions() (int64, error) {
	res, err := db.Exec(`DELETE FROM session`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Providers & credentials
// ---------------------------------------------------------------------------

// ListProviders returns providers with derived model counts.
func (db *DB) ListProviders() ([]model.Provider, error) {
	rows, err := db.Query(`
		SELECT p.id, p.name, p.type, p.base_url, p.status, p.enabled, p.last_check, COALESCE(p.last_error,''), p.created_at,
		       (SELECT COUNT(*) FROM model m WHERE m.provider_id = p.id) AS model_count,
		       COALESCE((SELECT c.hint FROM credential c WHERE c.provider_id = p.id LIMIT 1),'') AS hint
		FROM provider p ORDER BY p.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Provider{}
	for rows.Next() {
		var p model.Provider
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.BaseURL, &p.Status, &p.Enabled, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.ModelCount, &p.Credential); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProviderByID loads one provider.
func (db *DB) ProviderByID(id string) (*model.Provider, error) {
	var p model.Provider
	err := db.QueryRow(`
		SELECT p.id, p.name, p.type, p.base_url, p.status, p.enabled, p.last_check, COALESCE(p.last_error,''), p.created_at,
		       (SELECT COUNT(*) FROM model m WHERE m.provider_id = p.id),
		       COALESCE((SELECT c.hint FROM credential c WHERE c.provider_id = p.id LIMIT 1),'')
		FROM provider p WHERE p.id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Type, &p.BaseURL, &p.Status, &p.Enabled, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.ModelCount, &p.Credential)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

// CreateProvider inserts a provider.
func (db *DB) CreateProvider(p *model.Provider) error {
	_, err := db.Exec(`INSERT INTO provider (id, name, type, base_url, status, enabled, last_check, last_error, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Type, p.BaseURL, p.Status, boolToInt(p.Enabled), p.LastCheck, p.LastError, p.CreatedAt)
	return err
}

// UpdateProvider writes mutable provider fields.
func (db *DB) UpdateProvider(p *model.Provider) error {
	_, err := db.Exec(`UPDATE provider SET name=?, type=?, base_url=?, status=?, enabled=?, last_check=?, last_error=? WHERE id=?`,
		p.Name, p.Type, p.BaseURL, p.Status, boolToInt(p.Enabled), p.LastCheck, p.LastError, p.ID)
	return err
}

// SetProviderStatus updates status and last check timestamp.
func (db *DB) SetProviderStatus(id, status, lastErr string, at int64) error {
	_, err := db.Exec(`UPDATE provider SET status=?, last_error=?, last_check=? WHERE id=?`, status, lastErr, at, id)
	return err
}

// DeleteProvider removes a provider, its dependants (FK cascade), and every
// route that the deletion left without a single usable target.
//
// route_target cascades on model delete, but the route row does not: without
// this cleanup a deleted provider left "ghost" routes behind that still showed
// up with zero targets. Deleting is the honest outcome here - a route with no
// target can never serve traffic and cannot be repaired without picking a new
// model, so keeping it only hides broken state. Disabling a provider is the
// reversible path and keeps its routes intact.
func (db *DB) DeleteProvider(id string) (routesRemoved int64, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM provider WHERE id=?`, id); err != nil {
		return 0, err
	}
	// Sweep any route that no longer has a target. This also repairs routes
	// orphaned by earlier versions of the app.
	res, err := tx.Exec(`DELETE FROM route WHERE id NOT IN (SELECT DISTINCT route_id FROM route_target)`)
	if err != nil {
		return 0, err
	}
	routesRemoved, _ = res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return routesRemoved, nil
}

// SaveCredential upserts the encrypted credential for a provider.
func (db *DB) SaveCredential(providerID string, ct, nonce []byte, keyVersion int, hint string) error {
	_, err := db.Exec(`DELETE FROM credential WHERE provider_id=?`, providerID)
	if err != nil {
		return err
	}
	id := NewID("cred")
	_, err = db.Exec(`INSERT INTO credential (id, provider_id, ciphertext, nonce, key_version, hint) VALUES (?,?,?,?,?,?)`,
		id, providerID, ct, nonce, keyVersion, hint)
	return err
}

// CredentialFor returns ciphertext+nonce for a provider. Never logged.
func (db *DB) CredentialFor(providerID string) (ct, nonce []byte, err error) {
	err = db.QueryRow(`SELECT ciphertext, nonce FROM credential WHERE provider_id=? LIMIT 1`, providerID).Scan(&ct, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	return ct, nonce, err
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

const modelSelect = `SELECT m.id, m.provider_id, p.name, m.model_id, m.display_name, m.context_window,
	m.price_in, m.price_out, m.capabilities, m.available, p.status
	FROM model m JOIN provider p ON p.id = m.provider_id`

func scanModels(rows *sql.Rows) ([]model.Model, error) {
	defer rows.Close()
	out := []model.Model{}
	for rows.Next() {
		var m model.Model
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.ModelID, &m.DisplayName, &m.ContextWindow,
			&m.PriceIn, &m.PriceOut, &m.Capabilities, &m.Available, &m.Status); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListModels returns all models.
func (db *DB) ListModels() ([]model.Model, error) {
	rows, err := db.Query(modelSelect + ` ORDER BY p.name ASC, m.display_name ASC`)
	if err != nil {
		return nil, err
	}
	return scanModels(rows)
}

// ListModelsByProvider returns models for one provider.
func (db *DB) ListModelsByProvider(providerID string) ([]model.Model, error) {
	rows, err := db.Query(modelSelect+` WHERE m.provider_id = ? ORDER BY m.display_name ASC`, providerID)
	if err != nil {
		return nil, err
	}
	return scanModels(rows)
}

// ModelByID loads one model.
func (db *DB) ModelByID(id string) (*model.Model, error) {
	rows, err := db.Query(modelSelect+` WHERE m.id = ?`, id)
	if err != nil {
		return nil, err
	}
	list, err := scanModels(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// UpsertModel inserts or updates a discovered model.
func (db *DB) UpsertModel(m *model.Model) (string, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM model WHERE provider_id=? AND model_id=?`, m.ProviderID, m.ModelID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = NewID("mdl")
		_, err = db.Exec(`INSERT INTO model (id, provider_id, model_id, display_name, context_window, price_in, price_out, capabilities, available, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, id, m.ProviderID, m.ModelID, m.DisplayName, m.ContextWindow, m.PriceIn, m.PriceOut, m.Capabilities, boolToInt(m.Available), Now())
		return id, err
	}
	if err != nil {
		return "", err
	}
	_, err = db.Exec(`UPDATE model SET display_name=?, context_window=?, price_in=?, price_out=?, capabilities=?, available=? WHERE id=?`,
		m.DisplayName, m.ContextWindow, m.PriceIn, m.PriceOut, m.Capabilities, boolToInt(m.Available), id)
	return id, err
}

// DeleteModelsExcept removes models of a provider not present in keepIDs
// (used after a refresh-models to drop disappeared models).
func (db *DB) DeleteModelsExcept(providerID string, keepIDs []string) error {
	if len(keepIDs) == 0 {
		_, err := db.Exec(`DELETE FROM model WHERE provider_id=?`, providerID)
		return err
	}
	query := `DELETE FROM model WHERE provider_id=? AND id NOT IN (?` + repeat(",?", len(keepIDs)-1) + `)`
	args := []any{providerID}
	for _, id := range keepIDs {
		args = append(args, id)
	}
	_, err := db.Exec(query, args...)
	return err
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

// ListRoutes returns routes with targets.
//
// Routes without a single target are skipped: they are leftovers from a
// deleted provider (the old DELETE did not cascade) and cannot serve traffic.
// They are intentionally hidden from every operational view instead of being
// presented as "disabled", which would suggest they can be re-enabled.
func (db *DB) ListRoutes() ([]model.Route, error) {
	rows, err := db.Query(`SELECT r.id, r.alias, r.description, r.enabled, r.strategy, r.retry, r.timeout_ms,
		r.policy_id, COALESCE(p.name,''), r.fallback_on, r.created_at
		FROM route r LEFT JOIN policy p ON p.id = r.policy_id
		WHERE EXISTS (SELECT 1 FROM route_target t WHERE t.route_id = r.id)
		ORDER BY r.created_at ASC`)
	if err != nil {
		return nil, err
	}
	// Start with an empty slice, not a nil one: encoding/json renders nil as
	// `null`, and the SPA calls .map/.filter on this field. A fresh install
	// with zero routes used to blank the Routes/Activity/API Keys pages.
	routes := []model.Route{}
	for rows.Next() {
		var r model.Route
		if err := rows.Scan(&r.ID, &r.Alias, &r.Description, &r.Enabled, &r.Strategy, &r.Retry, &r.TimeoutMS,
			&r.PolicyID, &r.PolicyName, &r.FallbackOn, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		routes = append(routes, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range routes {
		targets, err := db.routeTargets(routes[i].ID)
		if err != nil {
			return nil, err
		}
		routes[i].Targets = targets
	}
	return routes, nil
}

func (db *DB) routeTargets(routeID string) ([]model.RouteTarget, error) {
	rows, err := db.Query(`SELECT t.id, t.route_id, t.model_id, t.position, t.role,
		m.model_id, m.provider_id, p.name, p.type, (m.available AND p.enabled)
		FROM route_target t
		JOIN model m ON m.id = t.model_id
		JOIN provider p ON p.id = m.provider_id
		WHERE t.route_id = ? ORDER BY CASE t.role WHEN 'primary' THEN 0 ELSE 1 END, t.position ASC`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.RouteTarget{}
	for rows.Next() {
		var t model.RouteTarget
		if err := rows.Scan(&t.ID, &t.RouteID, &t.ModelID, &t.Position, &t.Role, &t.ModelName, &t.ProviderID, &t.ProviderName, &t.ProviderType, &t.Available); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RouteByAlias resolves a route alias (case-insensitive).
func (db *DB) RouteByAlias(alias string) (*model.Route, error) {
	var r model.Route
	err := db.QueryRow(`SELECT r.id, r.alias, r.description, r.enabled, r.strategy, r.retry, r.timeout_ms,
		r.policy_id, COALESCE(p.name,''), r.fallback_on, r.created_at
		FROM route r LEFT JOIN policy p ON p.id = r.policy_id WHERE LOWER(r.alias) = LOWER(?)`, alias).
		Scan(&r.ID, &r.Alias, &r.Description, &r.Enabled, &r.Strategy, &r.Retry, &r.TimeoutMS, &r.PolicyID, &r.PolicyName, &r.FallbackOn, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Targets, err = db.routeTargets(r.ID)
	return &r, err
}

// RouteByID loads a route by id.
func (db *DB) RouteByID(id string) (*model.Route, error) {
	var alias string
	if err := db.QueryRow(`SELECT alias FROM route WHERE id=?`, id).Scan(&alias); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return db.RouteByAlias(alias)
}

// SaveRoute inserts or updates a route and replaces its targets atomically.
func (db *DB) SaveRoute(r *model.Route) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := Now()
	if r.CreatedAt == 0 {
		r.CreatedAt = now
	}
	// Defensive: an empty policy reference must be NULL. Passing a pointer to ""
	// used to violate the policy foreign key and surface as a raw save error.
	policyID := r.PolicyID
	if policyID != nil && strings.TrimSpace(*policyID) == "" {
		policyID = nil
	}
	if r.ID == "" {
		r.ID = NewID("rt")
		_, err = tx.Exec(`INSERT INTO route (id, alias, description, enabled, strategy, retry, timeout_ms, policy_id, fallback_on, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, r.ID, r.Alias, r.Description, boolToInt(r.Enabled), r.Strategy, r.Retry, r.TimeoutMS, policyID, r.FallbackOn, r.CreatedAt)
	} else {
		_, err = tx.Exec(`UPDATE route SET alias=?, description=?, enabled=?, strategy=?, retry=?, timeout_ms=?, policy_id=?, fallback_on=? WHERE id=?`,
			r.Alias, r.Description, boolToInt(r.Enabled), r.Strategy, r.Retry, r.TimeoutMS, policyID, r.FallbackOn, r.ID)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM route_target WHERE route_id=?`, r.ID); err != nil {
		return err
	}
	for i, t := range r.Targets {
		role := t.Role
		if role == "" {
			if i == 0 {
				role = "primary"
			} else {
				role = "fallback"
			}
		}
		pos := t.Position
		if pos == 0 {
			pos = i
		}
		_, err = tx.Exec(`INSERT INTO route_target (id, route_id, model_id, position, role) VALUES (?,?,?,?,?)`,
			NewID("rtg"), r.ID, t.ModelID, pos, role)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteRoute removes a route.
func (db *DB) DeleteRoute(id string) error {
	_, err := db.Exec(`DELETE FROM route WHERE id=?`, id)
	return err
}

// placeholders returns "?,?,…" for an IN list. Callers always pass a non-empty
// slice, so the generated SQL is never `IN ()`.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return "?" + repeat(",?", n-1)
}

// SetRoutesEnabled flips the enabled flag for several routes at once and
// reports how many rows changed. It backs the Routes page bulk toolbar so an
// operator does not have to toggle rows one by one.
func (db *DB) SetRoutesEnabled(ids []string, enabled bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, boolToInt(enabled))
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := db.Exec(`UPDATE route SET enabled=? WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteRoutes removes several routes in one statement (route_target rows go
// away through the foreign-key cascade).
func (db *DB) DeleteRoutes(ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := db.Exec(`DELETE FROM route WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

// ListAPIKeys returns keys with policy names.
func (db *DB) ListAPIKeys() ([]model.APIKey, error) {
	rows, err := db.Query(`SELECT k.id, k.name, k.prefix, k.enabled, k.created_at, k.expires_at, k.last_used_at,
		k.policy_id, COALESCE(p.name,''), k.allowed_routes
		FROM api_key k LEFT JOIN policy p ON p.id = k.policy_id ORDER BY k.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.APIKey{}
	for rows.Next() {
		var k model.APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Enabled, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.PolicyID, &k.PolicyName, &k.AllowedRoutes); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// APIKeyByHash resolves a key by the hash of the presented secret.
func (db *DB) APIKeyByHash(hash string) (*model.APIKey, error) {
	var k model.APIKey
	err := db.QueryRow(`SELECT k.id, k.name, k.prefix, k.hash, k.enabled, k.created_at, k.expires_at, k.last_used_at,
		k.policy_id, COALESCE(p.name,''), k.allowed_routes
		FROM api_key k LEFT JOIN policy p ON p.id = k.policy_id WHERE k.hash = ?`, hash).
		Scan(&k.ID, &k.Name, &k.Prefix, &k.Hash, &k.Enabled, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.PolicyID, &k.PolicyName, &k.AllowedRoutes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &k, err
}

// CreateAPIKey inserts a key.
func (db *DB) CreateAPIKey(k *model.APIKey) error {
	_, err := db.Exec(`INSERT INTO api_key (id, name, prefix, hash, enabled, created_at, expires_at, last_used_at, policy_id, allowed_routes)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, k.ID, k.Name, k.Prefix, k.Hash, boolToInt(k.Enabled), k.CreatedAt, k.ExpiresAt, k.LastUsedAt, k.PolicyID, k.AllowedRoutes)
	return err
}

// UpdateAPIKey writes policy-ish fields.
func (db *DB) UpdateAPIKey(k *model.APIKey) error {
	_, err := db.Exec(`UPDATE api_key SET name=?, enabled=?, expires_at=?, policy_id=?, allowed_routes=? WHERE id=?`,
		k.Name, boolToInt(k.Enabled), k.ExpiresAt, k.PolicyID, k.AllowedRoutes, k.ID)
	return err
}

// TouchAPIKey records last use.
func (db *DB) TouchAPIKey(id string, at int64) error {
	_, err := db.Exec(`UPDATE api_key SET last_used_at=? WHERE id=?`, at, id)
	return err
}

// DeleteAPIKey removes a key (revoke is delete + audit elsewhere).
func (db *DB) DeleteAPIKey(id string) error {
	_, err := db.Exec(`DELETE FROM api_key WHERE id=?`, id)
	return err
}

// ---------------------------------------------------------------------------
// Policies & bindings
// ---------------------------------------------------------------------------

const policyCols = `id, name, rpm, tpm, burst, daily_tokens, monthly_tokens, max_concurrent, max_tokens_req,
	max_ips, max_devices, allowed_models, on_exceed, queue_max_s, cooldown_s, auto_block_n, auto_block_m, auto_block_s, created_at`

func scanPolicy(sc interface{ Scan(...any) error }) (model.Policy, error) {
	var p model.Policy
	err := sc.Scan(&p.ID, &p.Name, &p.RPM, &p.TPM, &p.Burst, &p.DailyTokens, &p.MonthlyTokens, &p.MaxConcurrent, &p.MaxTokensReq,
		&p.MaxIPs, &p.MaxDevices, &p.AllowedModels, &p.OnExceed, &p.QueueMaxS, &p.CooldownS, &p.AutoBlockN, &p.AutoBlockM, &p.AutoBlockS, &p.CreatedAt)
	return p, err
}

// ListPolicies returns policies with binding counts.
func (db *DB) ListPolicies() ([]model.Policy, error) {
	rows, err := db.Query(`SELECT ` + policyCols + `,
		(SELECT COUNT(*) FROM policy_binding b WHERE b.policy_id = policy.id)
		FROM policy ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Policy{}
	for rows.Next() {
		var p model.Policy
		if err := rows.Scan(&p.ID, &p.Name, &p.RPM, &p.TPM, &p.Burst, &p.DailyTokens, &p.MonthlyTokens, &p.MaxConcurrent, &p.MaxTokensReq,
			&p.MaxIPs, &p.MaxDevices, &p.AllowedModels, &p.OnExceed, &p.QueueMaxS, &p.CooldownS, &p.AutoBlockN, &p.AutoBlockM, &p.AutoBlockS, &p.CreatedAt, &p.BoundCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PolicyByID loads one policy.
func (db *DB) PolicyByID(id string) (*model.Policy, error) {
	p, err := scanPolicy(db.QueryRow(`SELECT `+policyCols+` FROM policy WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

// UpsertPolicy inserts or updates a policy.
func (db *DB) UpsertPolicy(p *model.Policy) error {
	if p.CreatedAt == 0 {
		p.CreatedAt = Now()
	}
	if p.ID == "" {
		p.ID = NewID("pol")
		_, err := db.Exec(`INSERT INTO policy (`+policyCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.Name, p.RPM, p.TPM, p.Burst, p.DailyTokens, p.MonthlyTokens, p.MaxConcurrent, p.MaxTokensReq,
			p.MaxIPs, p.MaxDevices, p.AllowedModels, p.OnExceed, p.QueueMaxS, p.CooldownS, p.AutoBlockN, p.AutoBlockM, p.AutoBlockS, p.CreatedAt)
		return err
	}
	_, err := db.Exec(`UPDATE policy SET name=?, rpm=?, tpm=?, burst=?, daily_tokens=?, monthly_tokens=?, max_concurrent=?,
		max_tokens_req=?, max_ips=?, max_devices=?, allowed_models=?, on_exceed=?, queue_max_s=?, cooldown_s=?,
		auto_block_n=?, auto_block_m=?, auto_block_s=? WHERE id=?`,
		p.Name, p.RPM, p.TPM, p.Burst, p.DailyTokens, p.MonthlyTokens, p.MaxConcurrent, p.MaxTokensReq,
		p.MaxIPs, p.MaxDevices, p.AllowedModels, p.OnExceed, p.QueueMaxS, p.CooldownS, p.AutoBlockN, p.AutoBlockM, p.AutoBlockS, p.ID)
	return err
}

// DeletePolicy removes a policy and its bindings.
func (db *DB) DeletePolicy(id string) error {
	_, err := db.Exec(`DELETE FROM policy WHERE id=?`, id)
	return err
}

// BindPolicy attaches a policy to a scope, replacing any existing binding for
// that object. Pass an empty policyID to clear the binding.
func (db *DB) BindPolicy(policyID, scopeType, scopeID string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id := scopeType + ":" + scopeID
	if _, err := tx.Exec(`DELETE FROM policy_binding WHERE id = ?`, id); err != nil {
		return err
	}
	if policyID != "" {
		if _, err := tx.Exec(`INSERT INTO policy_binding (id, policy_id, scope_type, scope_id) VALUES (?,?,?,?)`,
			id, policyID, scopeType, scopeID); err != nil {
			return err
		}
		switch scopeType {
		case "route":
			if _, err := tx.Exec(`UPDATE route SET policy_id=? WHERE id=?`, nullable(policyID), scopeID); err != nil {
				return err
			}
		case "api_key":
			if _, err := tx.Exec(`UPDATE api_key SET policy_id=? WHERE id=?`, nullable(policyID), scopeID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// PolicyForScope resolves the effective policy bound to a scope object.
func (db *DB) PolicyForScope(scopeType, scopeID string) (*model.Policy, error) {
	var pid string
	err := db.QueryRow(`SELECT policy_id FROM policy_binding WHERE id = ?`, scopeType+":"+scopeID).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return db.PolicyByID(pid)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---------------------------------------------------------------------------
// IP rules
// ---------------------------------------------------------------------------

// ListIPRules returns all IP rules.
func (db *DB) ListIPRules() ([]model.IPRule, error) {
	rows, err := db.Query(`SELECT r.id, r.scope_type, r.scope_id,
		CASE WHEN r.scope_type='api_key' THEN COALESCE((SELECT k.name FROM api_key k WHERE k.id=r.scope_id),'') ELSE '' END,
		r.cidr, r.action, r.note, r.expires_at, r.created_at
		FROM ip_rule r ORDER BY r.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.IPRule{}
	for rows.Next() {
		var r model.IPRule
		if err := rows.Scan(&r.ID, &r.ScopeType, &r.ScopeID, &r.ScopeName, &r.CIDR, &r.Action, &r.Note, &r.ExpiresAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateIPRule inserts a rule.
func (db *DB) CreateIPRule(r *model.IPRule) error {
	if r.ID == "" {
		r.ID = NewID("ipr")
	}
	r.CreatedAt = Now()
	_, err := db.Exec(`INSERT INTO ip_rule (id, scope_type, scope_id, cidr, action, note, expires_at, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		r.ID, r.ScopeType, r.ScopeID, r.CIDR, r.Action, r.Note, r.ExpiresAt, r.CreatedAt)
	return err
}

// DeleteIPRule removes a rule.
func (db *DB) DeleteIPRule(id string) error {
	_, err := db.Exec(`DELETE FROM ip_rule WHERE id=?`, id)
	return err
}

// ---------------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------------

// UpsertDevice records a device sighting and returns its ID.
func (db *DB) UpsertDevice(apiKeyID, fingerprint, ip, ua string) (string, error) {
	now := Now()
	var id string
	err := db.QueryRow(`SELECT id FROM device WHERE api_key_id=? AND fingerprint=?`, apiKeyID, fingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = NewID("dev")
		// Label defaults to a UAgent-derived short string so the UI is readable.
		_, err = db.Exec(`INSERT INTO device (id, api_key_id, fingerprint, label, last_ip, user_agent, first_seen, last_seen, blocked, requests)
			VALUES (?,?,?,?,?,?,?,?,0,1)`, id, apiKeyID, fingerprint, defaultDeviceLabel(ua), ip, ua, now, now)
		return id, err
	}
	if err != nil {
		return "", err
	}
	_, err = db.Exec(`UPDATE device SET last_ip=?, user_agent=?, last_seen=?, requests=requests+1 WHERE id=?`, ip, ua, now, id)
	return id, err
}

// ListDevices returns devices with key names.
func (db *DB) ListDevices() ([]model.Device, error) {
	rows, err := db.Query(`SELECT d.id, d.api_key_id, COALESCE(k.name,''), d.fingerprint, d.label, d.last_ip, d.user_agent,
		d.first_seen, d.last_seen, d.blocked, d.requests
		FROM device d LEFT JOIN api_key k ON k.id = d.api_key_id ORDER BY d.last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Device{}
	for rows.Next() {
		var d model.Device
		if err := rows.Scan(&d.ID, &d.APIKeyID, &d.APIKeyName, &d.Fingerprint, &d.Label, &d.LastIP, &d.UserAgent,
			&d.FirstSeen, &d.LastSeen, &d.Blocked, &d.Requests); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeviceByID loads one device.
func (db *DB) DeviceByID(id string) (*model.Device, error) {
	var d model.Device
	err := db.QueryRow(`SELECT d.id, d.api_key_id, COALESCE(k.name,''), d.fingerprint, d.label, d.last_ip, d.user_agent,
		d.first_seen, d.last_seen, d.blocked, d.requests FROM device d LEFT JOIN api_key k ON k.id=d.api_key_id WHERE d.id=?`, id).
		Scan(&d.ID, &d.APIKeyID, &d.APIKeyName, &d.Fingerprint, &d.Label, &d.LastIP, &d.UserAgent, &d.FirstSeen, &d.LastSeen, &d.Blocked, &d.Requests)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

// SetDeviceBlocked marks a device blocked/unblocked and optionally renames it.
func (db *DB) SetDeviceBlocked(id string, blocked bool) error {
	_, err := db.Exec(`UPDATE device SET blocked=? WHERE id=?`, boolToInt(blocked), id)
	return err
}

// RenameDevice updates a device label.
func (db *DB) RenameDevice(id, label string) error {
	_, err := db.Exec(`UPDATE device SET label=? WHERE id=?`, label, id)
	return err
}

// CountDevicesForKey counts devices seen for a key.
func (db *DB) CountDevicesForKey(apiKeyID string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM device WHERE api_key_id=?`, apiKeyID).Scan(&n)
	return n, err
}

// IsDeviceBlocked reports whether a fingerprint is blocked for a key.
func (db *DB) IsDeviceBlocked(apiKeyID, fingerprint string) (bool, error) {
	var b bool
	err := db.QueryRow(`SELECT blocked FROM device WHERE api_key_id=? AND fingerprint=?`, apiKeyID, fingerprint).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return b, err
}

func defaultDeviceLabel(ua string) string {
	if ua == "" {
		return "Unknown device"
	}
	switch {
	case containsFold(ua, "curl"):
		return "curl client"
	case containsFold(ua, "python"):
		return "Python client"
	case containsFold(ua, "node"):
		return "Node client"
	case containsFold(ua, "chrome"):
		return "Chrome browser"
	case containsFold(ua, "firefox"):
		return "Firefox browser"
	case containsFold(ua, "safari"):
		return "Safari browser"
	}
	if len(ua) > 40 {
		return ua[:40]
	}
	return ua
}

// ---------------------------------------------------------------------------
// Blocks
// ---------------------------------------------------------------------------

// ListBlocks returns active and expired block entries.
func (db *DB) ListBlocks() ([]model.BlockEntry, error) {
	rows, err := db.Query(`SELECT id, kind, value, scope_id, reason, until, created_at FROM block_entry ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.BlockEntry{}
	for rows.Next() {
		var b model.BlockEntry
		if err := rows.Scan(&b.ID, &b.Kind, &b.Value, &b.ScopeID, &b.Reason, &b.Until, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateBlock inserts a block entry.
func (db *DB) CreateBlock(b *model.BlockEntry) error {
	if b.ID == "" {
		b.ID = NewID("blk")
	}
	b.CreatedAt = Now()
	_, err := db.Exec(`INSERT INTO block_entry (id, kind, value, scope_id, reason, until, created_at) VALUES (?,?,?,?,?,?,?)`,
		b.ID, b.Kind, b.Value, b.ScopeID, b.Reason, b.Until, b.CreatedAt)
	return err
}

// DeleteBlock removes a block entry.
func (db *DB) DeleteBlock(id string) error {
	_, err := db.Exec(`DELETE FROM block_entry WHERE id=?`, id)
	return err
}

// PurgeExpiredBlocks removes block entries past their time.
func (db *DB) PurgeExpiredBlocks(now int64) error {
	_, err := db.Exec(`DELETE FROM block_entry WHERE until IS NOT NULL AND until <= ?`, now)
	return err
}

// ---------------------------------------------------------------------------
// Rate counters (snapshot persistence for Guard)
// ---------------------------------------------------------------------------

// SnapshotCounter writes a counter window to SQLite.
func (db *DB) SnapshotCounter(scopeKey string, windowStart int64, tokens, count float64) error {
	_, err := db.Exec(`INSERT INTO rate_counter (scope_key, window_start, tokens, count) VALUES (?,?,?,?)
		ON CONFLICT(scope_key, window_start) DO UPDATE SET tokens=excluded.tokens, count=excluded.count`,
		scopeKey, windowStart, tokens, count)
	return err
}

// LoadCounters returns counters whose window started at or after since.
func (db *DB) LoadCounters(since int64) (map[string][2]float64, map[string]int64, error) {
	rows, err := db.Query(`SELECT scope_key, window_start, tokens, count FROM rate_counter WHERE window_start >= ?`, since)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	vals := map[string][2]float64{}
	windows := map[string]int64{}
	for rows.Next() {
		var key string
		var window int64
		var tokens, count float64
		if err := rows.Scan(&key, &window, &tokens, &count); err != nil {
			return nil, nil, err
		}
		vals[key] = [2]float64{tokens, count}
		windows[key] = window
	}
	return vals, windows, rows.Err()
}

// PurgeCounters removes counter rows older than ts.
func (db *DB) PurgeCounters(ts int64) error {
	_, err := db.Exec(`DELETE FROM rate_counter WHERE window_start < ?`, ts)
	return err
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// GetSetting returns a setting value or "".
func (db *DB) GetSetting(key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM system_setting WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// AllSettings returns every setting as a map.
func (db *DB) AllSettings() (map[string]string, error) {
	rows, err := db.Query(`SELECT key, value FROM system_setting`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting upserts one setting.
func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(`INSERT INTO system_setting (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// SetSettings upserts many settings in one transaction.
func (db *DB) SetSettings(values map[string]string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for k, v := range values {
		if _, err := tx.Exec(`INSERT INTO system_setting (key, value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if equalFold(s[i:i+len(sub)], sub) {
				return true
			}
		}
		return false
	})()
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

var _ = fmt.Sprintf
