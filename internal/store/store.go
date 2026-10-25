// Package store owns the SQLite database: connection, pragmas, embedded schema
// and small helpers shared by repositories.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
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
func Open(path string) (*DB, error) {
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
	return nil
}

// Now returns the current unix timestamp in seconds.
func Now() int64 { return time.Now().Unix() }

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
