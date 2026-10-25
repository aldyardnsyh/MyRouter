// Package monitor implements the background health prober described in the
// PRD (P1): every interval it validates each enabled provider's credential and
// writes the resulting status badge back to the provider row.
package monitor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/secret"
	"github.com/myrouter/myrouter/internal/store"
)

// DefaultInterval is the probe cadence used when New receives a non-positive
// interval (PRD: every 5 minutes).
const DefaultInterval = 5 * time.Minute

// probeTimeout bounds a single upstream credential check.
const probeTimeout = 10 * time.Second

// startDelay lets the process finish booting before the first probe so the
// dashboard has status badges without waiting a full interval.
const startDelay = 10 * time.Second

// Monitor periodically probes enabled providers and records their health.
type Monitor struct {
	db       *store.DB
	registry *provider.Registry
	box      *secret.Box
	interval time.Duration
	log      *logx.Logger

	mu sync.RWMutex
	// allowPrivate records the SSRF setting the current registry was built
	// with, so a change triggers exactly one client rebuild.
	allowPrivate *bool
}

// New builds a health monitor. A non-positive interval means DefaultInterval.
func New(db *store.DB, registry *provider.Registry, box *secret.Box, interval time.Duration, log *logx.Logger) *Monitor {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Monitor{db: db, registry: registry, box: box, interval: interval, log: log}
}

// Start launches the probe loop. It returns immediately; the goroutine stops
// when ctx is cancelled.
func (m *Monitor) Start(ctx context.Context) {
	interval := m.interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	go func() {
		if m.log != nil {
			m.log.Info("health monitor started", logx.F("interval", interval.String()))
		}
		first := time.NewTimer(startDelay)
		defer first.Stop()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
				m.RunOnce(ctx)
			case <-ticker.C:
				m.RunOnce(ctx)
			}
		}
	}()
}

// RunOnce probes every enabled provider once. It is exported so the loop and
// tests share the exact same code path.
func (m *Monitor) RunOnce(ctx context.Context) {
	// The SSRF policy is a mutable setting; pick it up every cycle.
	m.syncClient()

	list, err := m.db.ListProviders()
	if err != nil {
		if m.log != nil {
			m.log.Warn("health monitor: list providers failed", logx.F("error", err.Error()))
		}
		return
	}
	for i := range list {
		if ctx.Err() != nil {
			return
		}
		p := list[i]
		if !p.Enabled {
			continue
		}
		status, msg := m.check(ctx, p)
		if err := m.db.SetProviderStatus(p.ID, status, msg, store.Now()); err != nil {
			if m.log != nil {
				m.log.Warn("health monitor: status write failed",
					logx.F("provider", p.ID), logx.F("error", err.Error()))
			}
			continue
		}
		if m.log != nil {
			m.log.Debug("health monitor: probed provider",
				logx.F("provider", p.ID), logx.F("status", status))
		}
	}
}

// check validates one provider's credential and maps the outcome to a status.
// The mapping mirrors the manual test endpoint (internal/api/providers.go).
func (m *Monitor) check(ctx context.Context, p model.Provider) (status, msg string) {
	apiKey, err := m.decrypt(p.ID)
	switch {
	case err != nil:
		return model.StatusInvalidCred, "credential unavailable"
	case apiKey == "":
		return model.StatusInvalidCred, "no API key configured"
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	adapter := m.adapter(p.Type)
	if adapter == nil {
		return model.StatusUnavailable, "no adapter for provider type"
	}
	if err := adapter.Validate(probeCtx, provider.Credentials{APIKey: apiKey, BaseURL: p.BaseURL}); err != nil {
		pe := provider.AsError(err)
		switch pe.Category {
		case provider.CatRateLimit:
			return model.StatusRateLimited, pe.Message
		case provider.CatAuth:
			return model.StatusInvalidCred, pe.Message
		case provider.CatTimeout:
			return model.StatusDegraded, pe.Message
		default:
			return model.StatusUnavailable, pe.Message
		}
	}
	return model.StatusConnected, ""
}

// decrypt loads and decrypts the provider credential. A missing credential is
// reported as an empty key, not an error.
func (m *Monitor) decrypt(providerID string) (string, error) {
	if m.box == nil {
		return "", errors.New("credential vault unavailable")
	}
	ct, nonce, err := m.db.CredentialFor(providerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return m.box.Decrypt(ct, nonce)
}

// adapter returns the adapter for a provider type under the registry lock.
func (m *Monitor) adapter(providerType string) provider.BaseProvider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.registry == nil {
		return nil
	}
	return m.registry.Get(providerType)
}

// syncClient re-reads allow_private_upstreams and rebuilds the guarded HTTP
// client (and the adapter registry bound to it) when the value changed, so the
// prober honours SSRF policy changes without a restart.
func (m *Monitor) syncClient() {
	allow := m.settingBool("allow_private_upstreams", false)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.allowPrivate != nil && *m.allowPrivate == allow {
		return
	}
	changed := m.allowPrivate != nil
	m.allowPrivate = &allow
	if !changed {
		// First cycle: the injected registry already carries the boot-time
		// setting, so just remember it.
		return
	}
	m.registry = provider.NewRegistry(provider.GuardedClient(allow, 0))
	if m.log != nil {
		m.log.Info("health monitor: upstream client rebuilt", logx.F("allow_private", allow))
	}
}

// settingBool reads a boolean setting, falling back to def when unset.
func (m *Monitor) settingBool(key string, def bool) bool {
	v, err := m.db.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	return v == "true" || v == "1"
}
