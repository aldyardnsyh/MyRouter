package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// UI cache invalidation.
//
// The dashboard is a SPA served from an embedded bundle. Hashed assets are
// immutable, so a browser can keep serving the previous bundle from cache
// after the app is rebuilt. index.html is already no-store, but some browsers
// still honour a bfcache/disk copy, and an open tab can keep the old JS alive.
//
// The first shell request after a version change answers with Clear-Site-Data:
// "cache" once, then records the version so subsequent requests stay silent.
// Only the HTTP cache is cleared - cookies (admin session), localStorage and
// IndexedDB are left alone.
var (
	uiVersion    string
	uiMarkerPath string
	uiClearMu    sync.Mutex
	uiCleared    bool
)

// initUICache wires the version marker into <dataDir>/.ui-version.
func initUICache(dataDir, version string) {
	uiVersion = version
	uiMarkerPath = filepath.Join(dataDir, ".ui-version")
}

// uiCacheNeedsClear reports true at most once per process, and only when the
// marker on disk does not already match the running version. It writes the
// marker immediately so a burst of requests cannot each send the header.
func uiCacheNeedsClear() bool {
	if uiMarkerPath == "" || uiVersion == "" {
		return false
	}
	uiClearMu.Lock()
	defer uiClearMu.Unlock()
	if uiCleared {
		return false
	}
	uiCleared = true
	raw, err := os.ReadFile(uiMarkerPath)
	if err == nil && strings.TrimSpace(string(raw)) == uiVersion {
		return false
	}
	_ = os.WriteFile(uiMarkerPath, []byte(uiVersion), 0o600)
	return true
}
