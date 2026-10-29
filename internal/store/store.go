// Package store owns the SQLite database: connection, pragmas, embedded schema
// and small helpers shared by repositories.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps *sql.DB with MyRouter-specific helpers.
type DB struct {
	*sql.DB
	path string
}

// Open opens (or creates) the SQLite database, applies pragmas and schema.
// Before opening, it migrates a larger legacy database (e.g. one created when
// DataDir was relative to the working directory) into place so there is only
// ever one live database.
func Open(path string) (*DB, error) {
	migrateLegacyDB(path)
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single writer keeps the in-memory counters and DB in lock-step and avoids
	// SQLITE_BUSY storms; reads are still fast for a self-hosted tool.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxLifetime(0)

	db := &DB{DB: sqlDB, path: path}
	if err := db.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) migrate() error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	// Adaptive reasoning budget (Adaptive Dual Reasoner): per-route effort
	// column. ALTER is idempotent via PRAGMA check so existing DBs upgrade.
	var hasEffort int
	_ = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('route') WHERE name='effort'`).Scan(&hasEffort)
	if hasEffort == 0 {
		if _, err := db.Exec(`ALTER TABLE route ADD COLUMN effort TEXT NOT NULL DEFAULT 'medium'`); err != nil {
			return fmt.Errorf("add route.effort: %w", err)
		}
	}
	// Normalize legacy/empty values.
	if _, err := db.Exec(`UPDATE route SET effort='medium' WHERE effort IS NULL OR TRIM(effort)='' OR LOWER(effort) NOT IN ('low','medium','high')`); err != nil {
		return fmt.Errorf("normalize route.effort: %w", err)
	}
	// Self-heal routes orphaned by older builds: DeleteProvider used to leave
	// the route row behind after its targets cascaded away. Those rows can
	// never serve traffic and used to surface as "routes hidden because their
	// provider is disabled", which was misleading.
	if _, err := db.Exec(`DELETE FROM route WHERE id NOT IN (SELECT DISTINCT route_id FROM route_target)`); err != nil {
		return fmt.Errorf("prune orphan routes: %w", err)
	}
	return nil
}

// Now returns the current unix timestamp in seconds.
func Now() int64 { return time.Now().Unix() }

// migrateLegacyDB moves a larger database created under a previous DataDir
// layout (e.g. "./data" relative to a different working directory) into the
// current path, so there is only ever one live database. It only acts when
// the current path has no meaningful database yet and a clearly larger one
// exists next to the current working directory.
func migrateLegacyDB(path string) {
	if st, err := os.Stat(path); err == nil && st.Size() > 64*1024 {
		return // current DB already has real data
	}
	candidates := []string{
		filepath.Join("data", "myrouter.db"),
		filepath.Join("..", "data", "myrouter.db"),
	}
	best := ""
	var bestSize int64
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil || abs == path {
			continue
		}
		st, err := os.Stat(abs)
		if err != nil || st.Size() <= 64*1024 {
			continue
		}
		if st.Size() > bestSize {
			best, bestSize = abs, st.Size()
		}
	}
	if best == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if data, err := os.ReadFile(best); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// NewID generates a short, sortable, URL-safe identifier.
func NewID(prefix string) string {
	return prefix + "_" + randomToken(12)
}

// boolToInt converts a Go bool to SQLite's 0/1 representation.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SplitCSV splits a comma separated column into a trimmed, non-empty slice.
func SplitCSV(s string) []string {
	out := []string{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	parts := strings.Split(s, ",")
	out = make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// JoinCSV joins values trimming empties.
func JoinCSV(values []string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return strings.Join(out, ",")
}
