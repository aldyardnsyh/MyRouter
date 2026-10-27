// Package modelspec provides official model metadata — context window, input
// and output prices per 1M tokens, and vision/tools/reasoning capabilities —
// sourced from the models.dev catalog.
//
// The catalog is deliberately small and works offline: New installs an
// embedded, hand-curated fallback snapshot, Load overlays the trimmed on-disk
// cache written by a previous Refresh, and Refresh fetches the full api.json,
// trims it to the fields MyRouter needs, and atomically rewrites the cache.
// Lookup is deterministic for a given catalog snapshot and never performs
// network I/O.
package modelspec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// defaultURL is the models.dev catalog endpoint.
	defaultURL = "https://models.dev/api.json"

	// cacheFileName is the trimmed snapshot cache written under dataDir.
	cacheFileName = "modelspec.json"

	// requestTimeout bounds a single api.json download.
	requestTimeout = 15 * time.Second

	// refreshInterval is how often Start re-fetches the catalog.
	refreshInterval = 24 * time.Hour

	// staleAfter is the maximum age of a cache file before Start refreshes it.
	staleAfter = 7 * 24 * time.Hour

	// maxResponseBytes bounds the catalog body (the real file is a few MB) so
	// a broken endpoint cannot exhaust memory.
	maxResponseBytes = 15 << 20
)

// Spec is the subset of official model metadata MyRouter persists for a model.
// Prices are US dollars per 1M tokens and may be nil when the catalog does not
// publish a cost.
type Spec struct {
	Context   int      `json:"context,omitempty"`
	PriceIn   *float64 `json:"price_in,omitempty"`
	PriceOut  *float64 `json:"price_out,omitempty"`
	Vision    bool     `json:"vision,omitempty"`
	Tools     bool     `json:"tools,omitempty"`
	Reasoning bool     `json:"reasoning,omitempty"`
}

// snapshot is the trimmed catalog: the exact shape stored in the cache file and
// kept in memory. Providers are keyed by their lowercase models.dev id.
type snapshot struct {
	Source    string                   `json:"source,omitempty"`
	FetchedAt time.Time                `json:"fetched_at"`
	Providers map[string]providerEntry `json:"providers"`
}

// providerEntry keeps only the fields MyRouter uses from a models.dev provider.
type providerEntry struct {
	Name   string                `json:"name,omitempty"`
	Models map[string]modelEntry `json:"models"`
}

// modelEntry keeps only the fields MyRouter uses from a models.dev model.
type modelEntry struct {
	Name string `json:"name,omitempty"`
	Spec
}

// Catalog holds a model metadata snapshot. It is safe for concurrent use.
type Catalog struct {
	dataDir string
	url     string
	client  *http.Client

	mu        sync.RWMutex
	providers map[string]providerEntry
	global    map[string]Spec
	updatedAt time.Time
	hasCache  bool

	startOnce sync.Once
}

// New creates a Catalog that caches its snapshot in dataDir (an empty dataDir
// keeps the catalog memory-only) and downloads from the default models.dev
// endpoint. The embedded fallback is loaded immediately so Lookup works before
// any cache load or network fetch.
func New(dataDir string) *Catalog {
	return NewWithURL(dataDir, defaultURL)
}

// NewWithURL is New with an override for the catalog URL; tests point it at an
// httptest.Server.
func NewWithURL(dataDir, url string) *Catalog {
	c := &Catalog{
		dataDir: dataDir,
		url:     url,
		client:  &http.Client{Timeout: requestTimeout},
	}
	c.mu.Lock()
	c.applyLocked(fallbackSnapshot())
	c.mu.Unlock()
	return c
}

// Load loads the trimmed cache file when present. It never performs network
// I/O. A missing cache is not an error; the embedded fallback stays in effect
// and is overlaid by cache entries when a cache is found.
func (c *Catalog) Load() error {
	if c.dataDir == "" {
		return nil
	}
	path := c.cachePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("modelspec: read cache %s: %w", path, err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("modelspec: parse cache %s: %w", path, err)
	}
	c.mu.Lock()
	c.applyLocked(&snap)
	c.hasCache = true
	c.mu.Unlock()
	return nil
}

// Start launches the background refresh loop: one refresh if the cache is
// missing or older than 7 days, then a refresh every 24 hours until ctx is
// cancelled. It never blocks the caller.
func (c *Catalog) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		go c.run(ctx)
	})
}

func (c *Catalog) run(ctx context.Context) {
	if c.needsRefresh() {
		c.refreshWithTimeout(ctx)
	}
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refreshWithTimeout(ctx)
		}
	}
}

func (c *Catalog) needsRefresh() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasCache {
		return true
	}
	return time.Since(c.updatedAt) > staleAfter
}

func (c *Catalog) refreshWithTimeout(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, 2*requestTimeout)
	defer cancel()
	_ = c.Refresh(rctx)
}

// Refresh downloads the catalog, trims it, rewrites the cache atomically, and
// replaces the in-memory snapshot. A failed Refresh leaves the previous
// snapshot untouched and is never surfaced through Lookup.
func (c *Catalog) Refresh(ctx context.Context) error {
	snap, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	if c.dataDir != "" {
		if err := c.writeCache(snap); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.applyLocked(snap)
	c.hasCache = true
	c.mu.Unlock()
	return nil
}

// UpdatedAt reports when the current snapshot was fetched (zero if the catalog
// is entirely empty). The embedded fallback carries its curation date.
func (c *Catalog) UpdatedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.updatedAt
}

// Count reports how many models the catalog knows across all providers.
func (c *Catalog) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, p := range c.providers {
		n += len(p.Models)
	}
	return n
}

// applyLocked rebuilds the in-memory snapshot as embedded fallback overlaid by
// snap. The fallback is cloned rather than mutated so every apply is idempotent.
func (c *Catalog) applyLocked(snap *snapshot) {
	c.providers = mergeProviders(fallbackSnapshot().Providers, snap.Providers)
	c.global = buildGlobalIndex(c.providers)
	c.updatedAt = snap.FetchedAt.UTC()
	if c.updatedAt.IsZero() {
		c.updatedAt = time.Now().UTC()
	}
}

func (c *Catalog) cachePath() string {
	return filepath.Join(c.dataDir, cacheFileName)
}

func (c *Catalog) fetch(ctx context.Context) (*snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("modelspec: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MyRouter-modelspec/1")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("modelspec: fetch %s: %w", c.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("modelspec: fetch %s: unexpected status %s", c.url, resp.Status)
	}
	var raw map[string]wireProvider
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("modelspec: decode %s: %w", c.url, err)
	}
	return trimProviders(c.url, raw), nil
}

// writeCache writes the trimmed snapshot via temp file + rename so a crash can
// never leave a partially written cache. The final file is mode 0600.
func (c *Catalog) writeCache(snap *snapshot) error {
	if err := os.MkdirAll(c.dataDir, 0o700); err != nil {
		return fmt.Errorf("modelspec: create cache dir: %w", err)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("modelspec: encode cache: %w", err)
	}
	tmp, err := os.CreateTemp(c.dataDir, ".modelspec-*.tmp")
	if err != nil {
		return fmt.Errorf("modelspec: create temp cache file: %w", err)
	}
	tmpName := tmp.Name()
	remove := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		remove()
		return fmt.Errorf("modelspec: chmod temp cache file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		remove()
		return fmt.Errorf("modelspec: write temp cache file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		remove()
		return fmt.Errorf("modelspec: sync temp cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		remove()
		return fmt.Errorf("modelspec: close temp cache file: %w", err)
	}
	if err := os.Rename(tmpName, c.cachePath()); err != nil {
		remove()
		return fmt.Errorf("modelspec: replace cache file: %w", err)
	}
	return nil
}

// wireProvider / wireModel mirror the models.dev api.json shape we consume.
// Everything else in the upstream object (attachments, temperature, release
// dates, ...) is dropped by the decoder and never reaches memory or cache.
type wireProvider struct {
	Name   string               `json:"name"`
	Models map[string]wireModel `json:"models"`
}

type wireModel struct {
	Name       string         `json:"name"`
	Limit      wireLimit      `json:"limit"`
	Cost       wireCost       `json:"cost"`
	Modalities wireModalities `json:"modalities"`
	ToolCall   bool           `json:"tool_call"`
	Reasoning  bool           `json:"reasoning"`
}

type wireLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type wireCost struct {
	Input  *float64 `json:"input"`
	Output *float64 `json:"output"`
}

type wireModalities struct {
	Input []string `json:"input"`
}

// trimProviders converts a decoded models.dev response into the bounded
// snapshot MyRouter keeps in memory and on disk.
func trimProviders(source string, raw map[string]wireProvider) *snapshot {
	providers := make(map[string]providerEntry, len(raw))
	for id, p := range raw {
		pid := normalizeProviderID(id)
		if pid == "" || len(p.Models) == 0 {
			continue
		}
		entry := providerEntry{
			Name:   p.Name,
			Models: make(map[string]modelEntry, len(p.Models)),
		}
		for mid, m := range p.Models {
			id := normalizeModelID(mid)
			if id == "" {
				continue
			}
			entry.Models[id] = modelEntry{
				Name: m.Name,
				Spec: Spec{
					Context:   m.Limit.Context,
					PriceIn:   m.Cost.Input,
					PriceOut:  m.Cost.Output,
					Vision:    hasModality(m.Modalities.Input, "image") || hasModality(m.Modalities.Input, "pdf"),
					Tools:     m.ToolCall,
					Reasoning: m.Reasoning,
				},
			}
		}
		if len(entry.Models) == 0 {
			continue
		}
		providers[pid] = entry
	}
	return &snapshot{Source: source, FetchedAt: time.Now().UTC(), Providers: providers}
}

func hasModality(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// mergeProviders overlays overlay on top of base without mutating either. A
// provider present in both keeps base-only models (so the offline fallback
// keeps working) while overlay models overwrite same-id entries.
func mergeProviders(base, overlay map[string]providerEntry) map[string]providerEntry {
	out := make(map[string]providerEntry, len(base)+len(overlay))
	for id, p := range base {
		out[id] = cloneProvider(p)
	}
	for id, p := range overlay {
		if cur, ok := out[id]; ok {
			models := make(map[string]modelEntry, len(cur.Models)+len(p.Models))
			for mid, m := range cur.Models {
				models[mid] = m
			}
			for mid, m := range p.Models {
				models[mid] = m
			}
			p.Models = models
			if p.Name == "" {
				p.Name = cur.Name
			}
		}
		out[id] = cloneProvider(p)
	}
	return out
}

func cloneProvider(p providerEntry) providerEntry {
	models := make(map[string]modelEntry, len(p.Models))
	for id, m := range p.Models {
		models[id] = m
	}
	return providerEntry{Name: p.Name, Models: models}
}

// buildGlobalIndex maps model id to the best available spec across providers.
// Ties are broken deterministically: canonical providers win over third-party
// hosts, then the lexicographically smallest provider id wins.
func buildGlobalIndex(providers map[string]providerEntry) map[string]Spec {
	type candidate struct {
		spec     Spec
		provider string
		rank     int
	}
	best := make(map[string]candidate)
	for pid, p := range providers {
		rank := globalProviderRank(pid)
		for mid, m := range p.Models {
			cur, ok := best[mid]
			if !ok || rank < cur.rank || (rank == cur.rank && pid < cur.provider) {
				best[mid] = candidate{spec: m.Spec, provider: pid, rank: rank}
			}
		}
	}
	out := make(map[string]Spec, len(best))
	for mid, c := range best {
		out[mid] = c.spec
	}
	return out
}

// globalProviderRank orders canonical models.dev providers ahead of mirrors.
func globalProviderRank(pid string) int {
	if rank, ok := canonicalProviderRank[pid]; ok {
		return rank
	}
	return len(canonicalProviderRank) + 1
}

var canonicalProviderRank = map[string]int{
	"openai":    0,
	"anthropic": 1,
	"google":    2,
	"deepseek":  3,
	"xai":       4,
}
