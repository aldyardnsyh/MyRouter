package skills

import (
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

func seedItem(t *testing.T, db *store.DB, skillID, title, body string) {
	t.Helper()
	_ = db.CreateSkillItem(&model.SkillItem{
		ID: store.NewID("ski"), SkillID: skillID, Kind: "doc", Source: title,
		Title: title, Content: body, CreatedAt: store.Now(),
	})
}

// Dedup: same passage bound via two skills is injected once; the second
// section reports duplicates skipped instead of re-injecting full text.
func TestBuildForDedupsPassages(t *testing.T) {
	db := newTestDB(t)
	dup := "shared passage about caddy deploy gateway proxy reverse"
	s1 := createSkill(t, db, "docs-a", model.SkillRetrieval, map[string]any{"top_k": 3, "max_chars": 2000})
	s2 := createSkill(t, db, "docs-b", model.SkillRetrieval, map[string]any{"top_k": 3, "max_chars": 2000})
	seedItem(t, db, s1.ID, "Caddy Deploy", dup)
	seedItem(t, db, s2.ID, "Caddy Deploy", dup)
	eff, err := db.EffectiveSkills("coding")
	if err != nil {
		t.Fatal(err)
	}
	res, err := BuildFor(db, eff, "how to deploy caddy gateway proxy?")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(res.Context, dup); got != 1 {
		t.Errorf("duplicate passage body injected %d times, want 1:\n%s", got, res.Context)
	}
	if !strings.Contains(strings.ToLower(res.Context), "duplicate") {
		t.Errorf("expected duplicates-skipped note, got:\n%s", res.Context)
	}
}
