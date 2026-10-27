package modelspec

import (
	_ "embed"
	"encoding/json"
)

// fallbackJSON is a small, hand-curated snapshot of the most common models so
// Lookup works with no cache and no network. Prices are USD per 1M tokens.
//
//go:embed fallback.json
var fallbackJSON []byte

// fallback is parsed once at startup. The catalog never mutates it: applyLocked
// merges overlays into a fresh map built from these entries.
var fallback = mustLoadFallback()

func mustLoadFallback() *snapshot {
	var snap snapshot
	if err := json.Unmarshal(fallbackJSON, &snap); err != nil {
		panic("modelspec: invalid embedded fallback.json: " + err.Error())
	}
	if snap.Providers == nil {
		snap.Providers = map[string]providerEntry{}
	}
	return &snap
}

// fallbackSnapshot returns the shared embedded fallback snapshot. Callers must
// treat it as read-only.
func fallbackSnapshot() *snapshot {
	return fallback
}
