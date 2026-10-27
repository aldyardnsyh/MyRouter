// Package modelmeta overlays official model metadata (context window, prices
// per 1M tokens, vision/tools/reasoning capabilities) from the models.dev
// catalog onto the models discovered from providers, and keeps already stored
// models fresh when the catalog updates.
package modelmeta

import (
	"context"
	"time"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/modelspec"
	"github.com/myrouter/myrouter/internal/store"
)

// backfillInterval re-applies catalog data to stored models. The catalog itself
// refreshes daily; six hours keeps prices reasonably fresh without churn.
const backfillInterval = 6 * time.Hour

// Apply mutates a discovered model with catalog metadata. Unknown models keep
// whatever the provider adapter reported.
func Apply(cat *modelspec.Catalog, p *model.Provider, m *model.Model) {
	if cat == nil || m == nil || p == nil {
		return
	}
	spec, ok := cat.Lookup(p.Type, p.Name, m.ModelID)
	if !ok {
		return
	}
	if spec.Context > 0 {
		m.ContextWindow = spec.Context
	}
	if spec.PriceIn != nil {
		m.PriceIn = spec.PriceIn
	}
	if spec.PriceOut != nil {
		m.PriceOut = spec.PriceOut
	}
	caps := store.SplitCSV(m.Capabilities)
	if spec.Vision {
		caps = appendUnique(caps, "vision")
	}
	if spec.Tools {
		caps = appendUnique(caps, "tools")
	}
	if spec.Reasoning {
		caps = appendUnique(caps, "reasoning")
	}
	if len(caps) == 0 {
		caps = []string{"chat"}
	}
	m.Capabilities = store.JoinCSV(caps)
}

// Backfill updates models that are already in the database (for example models
// discovered before the catalog was available). Returns how many changed.
func Backfill(db *store.DB, cat *modelspec.Catalog) (int, error) {
	if cat == nil {
		return 0, nil
	}
	providers, err := db.ListProviders()
	if err != nil {
		return 0, err
	}
	byID := make(map[string]*model.Provider, len(providers))
	for i := range providers {
		p := providers[i]
		byID[p.ID] = &p
	}
	models, err := db.ListModels()
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, m := range models {
		p := byID[m.ProviderID]
		if p == nil {
			continue
		}
		beforeCtx := m.ContextWindow
		var beforeIn, beforeOut *float64
		if m.PriceIn != nil {
			v := *m.PriceIn
			beforeIn = &v
		}
		if m.PriceOut != nil {
			v := *m.PriceOut
			beforeOut = &v
		}
		beforeCaps := m.Capabilities
		Apply(cat, p, &m)
		if m.ContextWindow == beforeCtx && samePtr(m.PriceIn, beforeIn) && samePtr(m.PriceOut, beforeOut) && m.Capabilities == beforeCaps {
			continue
		}
		if err := db.UpdateModelMeta(m.ID, m.ContextWindow, m.PriceIn, m.PriceOut, m.Capabilities); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// BackfillLoop runs one backfill immediately, then periodically until ctx ends.
// It is a no-op until the catalog has data, so a failed first download never
// blocks startup.
func BackfillLoop(ctx context.Context, db *store.DB, cat *modelspec.Catalog, log *logx.Logger) {
	if log == nil {
		log = logx.Default()
	}
	run := func() {
		n, err := Backfill(db, cat)
		if err != nil {
			log.Warn("model metadata backfill failed", logx.F("error", err.Error()))
			return
		}
		if n > 0 {
			log.Info("model metadata backfilled from catalog", logx.F("models", n), logx.F("catalog", cat.Count()))
		}
	}
	run()
	ticker := time.NewTicker(backfillInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func appendUnique(list []string, want string) []string {
	for _, v := range list {
		if v == want {
			return list
		}
	}
	return append(list, want)
}

func samePtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
