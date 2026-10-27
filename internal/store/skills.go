package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/myrouter/myrouter/internal/model"
)

// ListSkills returns every skill with its item count, bindings and sync state.
func (db *DB) ListSkills() ([]model.Skill, error) {
	rows, err := db.Query(`SELECT s.id, s.name, s.description, s.type, s.enabled, s.config, s.created_at,
		(SELECT COUNT(*) FROM skill_item i WHERE i.skill_id = s.id),
		COALESCE(st.last_sync_at, 0), COALESCE(st.last_sync_error, ''), COALESCE(st.last_sync_items, 0)
		FROM skill s LEFT JOIN skill_state st ON st.skill_id = s.id
		ORDER BY s.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Skill{}
	for rows.Next() {
		var s model.Skill
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Type, &s.Enabled, &s.Config, &s.CreatedAt,
			&s.ItemCount, &s.SyncAt, &s.SyncError, &s.SyncItems); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	bindings, err := db.AllSkillBindings()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if b, ok := bindings[out[i].ID]; ok {
			out[i].Bindings = b
		} else {
			out[i].Bindings = model.SkillBindings{Routes: []string{}}
		}
	}
	return out, nil
}

// ListEnabledSkills returns the skills that should run on a request.
func (db *DB) ListEnabledSkills() ([]model.Skill, error) {
	all, err := db.ListSkills()
	if err != nil {
		return nil, err
	}
	out := make([]model.Skill, 0, len(all))
	for _, s := range all {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out, nil
}

// EffectiveSkills returns the enabled skills that apply to one route alias:
// global bindings plus bindings for that specific alias.
func (db *DB) EffectiveSkills(routeAlias string) ([]model.Skill, error) {
	rows, err := db.Query(`SELECT s.id, s.name, s.description, s.type, s.enabled, s.config, s.created_at
		FROM skill s
		WHERE s.enabled = 1 AND (
			EXISTS (SELECT 1 FROM skill_binding b WHERE b.skill_id = s.id AND b.scope_type = 'global')
			OR EXISTS (SELECT 1 FROM skill_binding b WHERE b.skill_id = s.id AND b.scope_type = 'route' AND b.scope_id = ?)
		)
		ORDER BY s.created_at ASC`, strings.ToLower(strings.TrimSpace(routeAlias)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Skill{}
	for rows.Next() {
		var s model.Skill
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Type, &s.Enabled, &s.Config, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SkillByID loads one skill.
func (db *DB) SkillByID(id string) (*model.Skill, error) {
	var s model.Skill
	err := db.QueryRow(`SELECT s.id, s.name, s.description, s.type, s.enabled, s.config, s.created_at,
		(SELECT COUNT(*) FROM skill_item i WHERE i.skill_id = s.id),
		COALESCE(st.last_sync_at, 0), COALESCE(st.last_sync_error, ''), COALESCE(st.last_sync_items, 0)
		FROM skill s LEFT JOIN skill_state st ON st.skill_id = s.id WHERE s.id = ?`, id).
		Scan(&s.ID, &s.Name, &s.Description, &s.Type, &s.Enabled, &s.Config, &s.CreatedAt,
			&s.ItemCount, &s.SyncAt, &s.SyncError, &s.SyncItems)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Bindings = db.SkillBindingsFor(s.ID)
	return &s, nil
}

// SkillByName is used by the idempotent built-in seed.
func (db *DB) SkillByName(name string) (*model.Skill, error) {
	var s model.Skill
	err := db.QueryRow(`SELECT id, name, description, type, enabled, config, created_at FROM skill WHERE name = ?`, name).
		Scan(&s.ID, &s.Name, &s.Description, &s.Type, &s.Enabled, &s.Config, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

func (db *DB) CreateSkill(s *model.Skill) error {
	_, err := db.Exec(`INSERT INTO skill (id, name, description, type, enabled, config, created_at)
		VALUES (?,?,?,?,?,?,?)`, s.ID, s.Name, s.Description, s.Type, boolToInt(s.Enabled), s.Config, s.CreatedAt)
	return err
}

func (db *DB) UpdateSkill(s *model.Skill) error {
	_, err := db.Exec(`UPDATE skill SET name=?, description=?, type=?, enabled=?, config=? WHERE id=?`,
		s.Name, s.Description, s.Type, boolToInt(s.Enabled), s.Config, s.ID)
	return err
}

func (db *DB) DeleteSkill(id string) error {
	_, err := db.Exec(`DELETE FROM skill WHERE id=?`, id)
	return err
}

// SkillBindingsFor loads the bindings of one skill.
func (db *DB) SkillBindingsFor(skillID string) model.SkillBindings {
	out := model.SkillBindings{Routes: []string{}}
	rows, err := db.Query(`SELECT scope_type, scope_id FROM skill_binding WHERE skill_id=? ORDER BY scope_type, scope_id`, skillID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var scopeType, scopeID string
		if err := rows.Scan(&scopeType, &scopeID); err != nil {
			return out
		}
		if scopeType == "global" {
			out.Global = true
		} else if scopeType == "route" && scopeID != "" {
			out.Routes = append(out.Routes, scopeID)
		}
	}
	return out
}

// AllSkillBindings maps skill id to its bindings in one query.
func (db *DB) AllSkillBindings() (map[string]model.SkillBindings, error) {
	out := map[string]model.SkillBindings{}
	rows, err := db.Query(`SELECT skill_id, scope_type, scope_id FROM skill_binding ORDER BY skill_id, scope_type, scope_id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var skillID, scopeType, scopeID string
		if err := rows.Scan(&skillID, &scopeType, &scopeID); err != nil {
			return out, err
		}
		b := out[skillID]
		if b.Routes == nil {
			b.Routes = []string{}
		}
		if scopeType == "global" {
			b.Global = true
		} else if scopeType == "route" && scopeID != "" {
			b.Routes = append(b.Routes, scopeID)
		}
		out[skillID] = b
	}
	return out, rows.Err()
}

// SetSkillBindings replaces every binding of a skill in one transaction.
func (db *DB) SetSkillBindings(skillID string, global bool, routes []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM skill_binding WHERE skill_id=?`, skillID); err != nil {
		return err
	}
	if global {
		if _, err := tx.Exec(`INSERT INTO skill_binding (id, skill_id, scope_type, scope_id) VALUES (?,?,?,?)`,
			NewID("skb"), skillID, "global", ""); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, alias := range routes {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true
		if _, err := tx.Exec(`INSERT INTO skill_binding (id, skill_id, scope_type, scope_id) VALUES (?,?,?,?)`,
			NewID("skb"), skillID, "route", alias); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SkillState returns the last ingest result of a skill.
func (db *DB) SkillState(skillID string) (int64, string, int, error) {
	var at int64
	var errMsg string
	var items int
	err := db.QueryRow(`SELECT last_sync_at, last_sync_error, last_sync_items FROM skill_state WHERE skill_id=?`, skillID).
		Scan(&at, &errMsg, &items)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", 0, nil
	}
	return at, errMsg, items, err
}

// SetSkillState upserts the last ingest result of a skill.
func (db *DB) SetSkillState(skillID string, at int64, errMsg string, items int) error {
	_, err := db.Exec(`INSERT INTO skill_state (skill_id, last_sync_at, last_sync_error, last_sync_items)
		VALUES (?,?,?,?)
		ON CONFLICT(skill_id) DO UPDATE SET last_sync_at=excluded.last_sync_at,
			last_sync_error=excluded.last_sync_error, last_sync_items=excluded.last_sync_items`,
		skillID, at, errMsg, items)
	return err
}

// ListSkillItems returns items newest first. Retrieval callers score them in
// memory; the cap keeps one skill from loading an unbounded corpus per request.
func (db *DB) ListSkillItems(skillID string, limit int) ([]model.SkillItem, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := db.Query(`SELECT id, skill_id, kind, source, title, content, created_at
		FROM skill_item WHERE skill_id=? ORDER BY created_at DESC, rowid DESC LIMIT ?`, skillID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SkillItem{}
	for rows.Next() {
		var it model.SkillItem
		if err := rows.Scan(&it.ID, &it.SkillID, &it.Kind, &it.Source, &it.Title, &it.Content, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SearchSkillItems filters items by a substring and returns the filtered total.
func (db *DB) SearchSkillItems(skillID, q string, limit int) ([]model.SkillItem, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q = strings.TrimSpace(q)
	where := `skill_id=?`
	args := []any{skillID}
	if q != "" {
		where += ` AND (title LIKE ? OR source LIKE ? OR content LIKE ?)`
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skill_item WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(`SELECT id, skill_id, kind, source, title, content, created_at
		FROM skill_item WHERE `+where+` ORDER BY created_at DESC, rowid DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.SkillItem{}
	for rows.Next() {
		var it model.SkillItem
		if err := rows.Scan(&it.ID, &it.SkillID, &it.Kind, &it.Source, &it.Title, &it.Content, &it.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// MatchSkillItems returns items containing at least one term anywhere in title,
// source or content. SQLite does the substring scan in C, which is much cheaper
// than loading a large corpus into Go on every request; the caller then ranks
// only the matched rows.
func (db *DB) MatchSkillItems(skillID string, terms []string, limit int) ([]model.SkillItem, error) {
	if limit <= 0 || limit > maxSkillScan {
		limit = maxSkillScan
	}
	where := `skill_id=?`
	args := []any{skillID}
	groups := make([]string, 0, len(terms))
	for _, t := range terms {
		if strings.TrimSpace(t) == "" {
			continue
		}
		groups = append(groups, `(title LIKE ? OR source LIKE ? OR content LIKE ?)`)
		like := "%" + t + "%"
		args = append(args, like, like, like)
	}
	if len(groups) > 0 {
		where += ` AND (` + strings.Join(groups, " OR ") + `)`
	}
	rows, err := db.Query(`SELECT id, skill_id, kind, source, title, content, created_at
		FROM skill_item WHERE `+where+` ORDER BY created_at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SkillItem{}
	for rows.Next() {
		var it model.SkillItem
		if err := rows.Scan(&it.ID, &it.SkillID, &it.Kind, &it.Source, &it.Title, &it.Content, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// maxSkillScan bounds how many rows a retrieval skill may load per request.
const maxSkillScan = 2000

func (db *DB) CreateSkillItem(it *model.SkillItem) error {
	_, err := db.Exec(`INSERT INTO skill_item (id, skill_id, kind, source, title, content, created_at)
		VALUES (?,?,?,?,?,?,?)`, it.ID, it.SkillID, it.Kind, it.Source, it.Title, it.Content, it.CreatedAt)
	return err
}

// CreateSkillItems inserts a batch in one transaction (ingest path).
func (db *DB) CreateSkillItems(items []model.SkillItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO skill_item (id, skill_id, kind, source, title, content, created_at)
		VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, it := range items {
		if _, err := stmt.Exec(it.ID, it.SkillID, it.Kind, it.Source, it.Title, it.Content, it.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) DeleteSkillItem(skillID, itemID string) error {
	_, err := db.Exec(`DELETE FROM skill_item WHERE id=? AND skill_id=?`, itemID, skillID)
	return err
}

// ClearSkillItemsByKind deletes every item of one kind for a skill. Ingest uses
// this to replace its derived docs without touching captured notes.
func (db *DB) ClearSkillItemsByKind(skillID, kind string) error {
	_, err := db.Exec(`DELETE FROM skill_item WHERE skill_id=? AND kind=?`, skillID, kind)
	return err
}

// TrimSkillItems keeps the newest keep items of one kind and drops the rest.
func (db *DB) TrimSkillItems(skillID, kind string, keep int) error {
	if keep <= 0 {
		return nil
	}
	_, err := db.Exec(`DELETE FROM skill_item WHERE skill_id=? AND kind=? AND id NOT IN (
		SELECT id FROM skill_item WHERE skill_id=? AND kind=?
		ORDER BY created_at DESC, rowid DESC LIMIT ?)`, skillID, kind, skillID, kind, keep)
	return err
}

// VisionCacheGet returns a cached image description.
func (db *DB) VisionCacheGet(hash string) (string, bool) {
	var desc string
	err := db.QueryRow(`SELECT description FROM skill_vision_cache WHERE hash=?`, hash).Scan(&desc)
	if err != nil || desc == "" {
		return "", false
	}
	return desc, true
}

// VisionCachePut caches an image description (LRU-ish cleanup by age/count).
func (db *DB) VisionCachePut(hash, description string) error {
	if _, err := db.Exec(`INSERT INTO skill_vision_cache (hash, description, created_at) VALUES (?,?,?)
		ON CONFLICT(hash) DO UPDATE SET description=excluded.description`, hash, description, Now()); err != nil {
		return err
	}
	// Bound the cache: keep the newest 500 descriptions.
	_, err := db.Exec(`DELETE FROM skill_vision_cache WHERE hash NOT IN (
		SELECT hash FROM skill_vision_cache ORDER BY created_at DESC LIMIT 500)`)
	return err
}

// builtinSkillNames marks the seeded skills. They may be edited and disabled
// but not deleted, because the seeder would recreate them on the next start.
var builtinSkillNames = map[string]bool{
	"xerj": true, "ai-memory": true, "chat-knowledge": true,
	"vision": true, "memory": true, "core": true,
}

// IsBuiltinSkill reports whether name belongs to a seeded skill.
func IsBuiltinSkill(name string) bool { return builtinSkillNames[name] }

// SeedBuiltinSkills inserts the built-in skills once and binds them globally so
// every model alias (including routes created later) gets them automatically.
// It never overwrites a user edit; a pre-existing skill with no bindings gets
// the global default so older databases upgrade cleanly.
func (db *DB) SeedBuiltinSkills() error {
	builtins := []struct {
		Skill  model.Skill
		Global bool
	}{
		{
			Skill: model.Skill{
				Name:        "xerj",
				Description: "Reference-coding memory. Index a local repo or cloned-library tree and inject the most relevant code passages before answering.",
				Type:        model.SkillRetrieval,
				Enabled:     true,
				Config:      `{"top_k":5,"max_chars":6000,"sync_glob":"*.{go,ts,tsx,js,jsx,py,rs,md,toml,yaml,yml,json}","sync_paths":[]}`,
			},
			Global: true,
		},
		{
			Skill: model.Skill{
				Name:        "ai-memory",
				Description: "Long-term project memory from an ai-memory wiki. Point sync_paths at its git-backed markdown wiki to inject relevant pages.",
				Type:        model.SkillRetrieval,
				Enabled:     true,
				Config:      `{"top_k":6,"max_chars":6000,"sync_glob":"*.md","sync_paths":[]}`,
			},
			Global: true,
		},
		{
			Skill: model.Skill{
				Name:        "chat-knowledge",
				Description: "Learns from past requests. Captures question/answer pairs and injects the most similar ones into later prompts.",
				Type:        model.SkillChat,
				Enabled:     true,
				Config:      `{"top_k":4,"max_chars":4000,"limit":20,"capture_max_chars":2000,"capture_keep":1000}`,
			},
			Global: true,
		},
		{
			Skill: model.Skill{
				Name:        "vision",
				Description: "Vision bridge. Describes images with a vision-capable model so text-only models can answer image prompts.",
				Type:        model.SkillVision,
				Enabled:     true,
				Config:      `{"vision_route":"auto","max_images":4,"max_chars":1500,"vision_prompt":"Describe this image precisely for a text-only model. Include visible text verbatim, UI elements, chart values and any details needed to answer a follow-up question."}`,
			},
			Global: true,
		},
		{
			Skill: model.Skill{
				Name:        "memory",
				Description: "Long-term notes and decisions. Inject the newest manual notes so context survives across sessions.",
				Type:        model.SkillMemory,
				Enabled:     true,
				Config:      `{"limit":20,"max_chars":5000}`,
			},
			Global: true,
		},
		{
			Skill: model.Skill{
				Name:        "core",
				Description: "Optional house rules applied to every request. Disabled by default.",
				Type:        model.SkillPrompt,
				Enabled:     false,
				Config:      `{"prompt":"","max_chars":2000}`,
			},
			Global: true,
		},
	}
	for i := range builtins {
		b := builtins[i].Skill
		existing, err := db.SkillByName(b.Name)
		if err == nil {
			// Existing skill: never overwrite config/enabled. Only adopt the
			// global default binding when the skill has no bindings at all.
			bindings := db.SkillBindingsFor(existing.ID)
			if !bindings.Global && len(bindings.Routes) == 0 {
				if err := db.SetSkillBindings(existing.ID, true, nil); err != nil {
					return err
				}
			}
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		b.ID = NewID("skl")
		b.CreatedAt = Now()
		if strings.TrimSpace(b.Config) == "" {
			b.Config = "{}"
		}
		if err := db.CreateSkill(&b); err != nil {
			return err
		}
		if builtins[i].Global {
			if err := db.SetSkillBindings(b.ID, true, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
