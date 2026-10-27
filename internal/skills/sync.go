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

// syncDue runs every enabled retrieval skill whose configured interval elapsed.
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
