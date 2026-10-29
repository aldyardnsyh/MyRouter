// Package config loads runtime configuration from environment variables and
// the settings table. Precedence: env > database settings > defaults.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds process-level configuration.
type Config struct {
	Addr         string
	DataDir      string
	MasterKey    string // base64 (32 bytes) or a passphrase hashed with SHA-256
	DevMode      bool
	SessionTTL   int64 // seconds
	LogRetention int   // days
}

// Load reads configuration from the environment.
//
// DataDir defaults to a "data" folder next to the running executable (not the
// working directory), so the desktop build, the console build and any tooling
// always share one database no matter where the process was launched from.
// MYROUTER_DATA still overrides when set explicitly.
func Load() Config {
	cfg := Config{
		Addr:         env("MYROUTER_ADDR", ":20128"),
		DataDir:      env("MYROUTER_DATA", exeDataDir()),
		MasterKey:    os.Getenv("MYROUTER_MASTER_KEY"),
		DevMode:      env("MYROUTER_ENV", "production") == "development",
		SessionTTL:   7 * 24 * 3600,
		LogRetention: 30,
	}
	if v := os.Getenv("MYROUTER_SESSION_TTL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SessionTTL = int64(n) * 3600
		}
	}
	if v := os.Getenv("MYROUTER_LOG_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.LogRetention = n
		}
	}
	return cfg
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// exeDataDir returns "<exe-dir>/data", falling back to "./data" when the
// executable path cannot be resolved (tests, go run without a binary path).
func exeDataDir() string {
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			return filepath.Join(dir, "data")
		}
	}
	return "./data"
}
