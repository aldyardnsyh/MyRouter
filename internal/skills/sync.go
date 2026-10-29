package skills

import (
	"context"
	"time"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// Syncer periodically re-ingests retrieval skills that have sync_paths, so a
// local code tree or an ai-memory wiki stays fresh without manual clicks.
type Syncer struct {
	db  *store.DB
	log *logx.Logger
}

// NewSyncer builds a background syncer.
func NewSyncer(db *store.DB, log *logx.Logger) *Syncer {
	if log == nil {
		log = logx.Default()
	}
	return &Syncer{db: db, log: log}
}

// Run blocks until ctx is cancelled, syncing due skills every minute. The
// initial pass is delayed so server startup and first requests stay fast.
func (s *Syncer) Run(ctx context.Context) {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.syncDue(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncDue(ctx)
		}
	}
}

// EnsureFreshForRequest syncs retrieval skills inline when they have never
// been synced, and triggers a background refresh when the configured interval
// elapsed. It runs at the start of a user request so the flow is:
// input -> fresh skills -> LLM -> response already backed by skills -> output.
// Never-synced skills block briefly (one-time cost); due-but-stale skills
// refresh in the background while the current request uses cached items
// (fail-open: sync never blocks or fails the user request).
func EnsureFreshForRequest(db *store.DB, log *logx.Logger, effective []model.Skill) {
	if log == nil {
		log = logx.Default()
	}
	now := store.Now()
	for _, sk := range effective {
		if sk.Type != model.SkillRetrieval {
			continue
		}
		cfg := ParseConfig(sk.Config)
		if len(cleanSyncPaths(cfg.SyncPaths)) == 0 {
			continue
		}
		last, _, _, stateErr := db.SkillState(sk.ID)
		if stateErr != nil {
			continue
		}
		if last == 0 {
			report := SyncSkill(db, sk)
			log.Info("skill synced on-request (first)",
				logx.F("skill", sk.Name), logx.F("files", report.Files),
				logx.F("items", report.Items), logx.F("error", report.Error))
			continue
		}
		if now-last < int64(cfg.SyncIntervalM)*60 {
			continue
		}
		go func(s model.Skill) {
			report := SyncSkill(db, s)
			l := log.Info
			if report.Error != "" {
				l = log.Warn
			}
			l("skill synced on-request (background)",
				logx.F("skill", s.Name), logx.F("files", report.Files),
				logx.F("items", report.Items), logx.F("error", report.Error))
		}(sk)
		// Mark state now so concurrent requests don't spawn duplicate syncs;
		// the background run overwrites with the real result.
		_ = db.SetSkillState(sk.ID, now, "", 0)
	}
}
func (s *Syncer) syncDue(ctx context.Context) {
	list, err := s.db.ListEnabledSkills()
	if err != nil {
		s.log.Warn("skill sync: list failed", logx.F("error", err.Error()))
		return
	}
	now := store.Now()
	for _, sk := range list {
		if sk.Type != model.SkillRetrieval {
			continue
		}
		cfg := ParseConfig(sk.Config)
		if len(cleanSyncPaths(cfg.SyncPaths)) == 0 {
			continue
		}
		last, _, _, stateErr := s.db.SkillState(sk.ID)
		if stateErr != nil {
			continue
		}
		if last > 0 && now-last < int64(cfg.SyncIntervalM)*60 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		report := SyncSkill(s.db, sk)
		log := s.log.Info
		if report.Error != "" {
			log = s.log.Warn
		}
		log("skill synced",
			logx.F("skill", sk.Name), logx.F("files", report.Files),
			logx.F("items", report.Items), logx.F("error", report.Error))
	}
}
