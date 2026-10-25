package main

import (
	"crypto/rand"
	"io"
	"strconv"
	"strings"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// cryptoRandRead fills buf with cryptographically secure random bytes.
func cryptoRandRead(buf []byte) (int, error) { return io.ReadFull(rand.Reader, buf) }

func settingString(db *store.DB, key, def string) string {
	v, err := db.GetSetting(key)
	if err != nil || strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func settingBool(db *store.DB, key string, def bool) bool {
	v := settingString(db, key, "")
	if v == "" {
		return def
	}
	return v == "true" || v == "1"
}

func parseInt(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

// seedDefaults writes first-run settings and the built-in policy presets.
// It is idempotent and never overwrites user choices.
func seedDefaults(db *store.DB) error {
	defaults := map[string]string{
		"api_port":                "20128",
		"session_ttl_hours":       "168",
		"default_retry":           "1",
		"default_timeout_ms":      "30000",
		"log_retention_days":      "30",
		"store_request_body":      "false",
		"allow_private_upstreams": "false",
		"timezone":                "UTC",
		"theme":                   "system",
		"cors_origins":            "",
		"trusted_proxies":         "",
	}
	for k, v := range defaults {
		existing, err := db.GetSetting(k)
		if err != nil {
			return err
		}
		if existing == "" {
			if err := db.SetSetting(k, v); err != nil {
				return err
			}
		}
	}

	policies, err := db.ListPolicies()
	if err != nil {
		return err
	}
	if len(policies) > 0 {
		return nil
	}
	presets := []struct {
		name string
		rpm  int
		tpm  int
	}{
		{"Unlimited", 0, 0},
		{"Personal", 60, 120000},
		{"Team Member", 120, 240000},
		{"Strict", 10, 20000},
	}
	for _, p := range presets {
		pl := &model.Policy{Name: p.name, OnExceed: "reject"}
		pl.RPM = p.rpm
		pl.TPM = p.tpm
		pl.Burst = p.rpm / 2
		if p.name == "Strict" {
			pl.MaxConcurrent = 2
			pl.MaxIPs = 1
			pl.MaxDevices = 1
			pl.AutoBlockN = 20
			pl.AutoBlockM = 5
			pl.AutoBlockS = 300
		}
		if err := db.UpsertPolicy(pl); err != nil {
			return err
		}
	}
	return nil
}
