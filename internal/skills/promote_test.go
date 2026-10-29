package skills

import (
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

func seedChatMemory(t *testing.T, db *store.DB) (*model.Skill, *model.Skill) {
	t.Helper()
	chat := &model.Skill{ID: store.NewID("skl"), Name: "chat", Description: "chat",
		Type: model.SkillChat, Enabled: true, Config: `{}`, CreatedAt: store.Now()}
	if err := db.CreateSkill(chat); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSkillBindings(chat.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	mem := &model.Skill{ID: store.NewID("skl"), Name: "mem", Description: "mem",
		Type: model.SkillMemory, Enabled: true, Config: `{"limit":20}`, CreatedAt: store.Now()}
	if err := db.CreateSkill(mem); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSkillBindings(mem.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	cs, _ := db.SkillByID(chat.ID)
	ms, _ := db.SkillByID(mem.ID)
	return cs, ms
}

func TestPromoteAfter3SamePattern(t *testing.T) {
	db := newTestDB(t)
	chat, mem := seedChatMemory(t, db)
	q := "Selalu pakai bahasa Indonesia untuk jawaban"
	for i := 0; i < 3; i++ {
		if err := Capture(db, *chat, "coding", q, "ok jawaban"); err != nil {
			t.Fatal(err)
		}
	}
	items, err := db.ListSkillItems(mem.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 promoted memory item, got %d", len(items))
	}
	if items[0].Source != "promoted from chat" {
		t.Fatalf("wrong source: %q", items[0].Source)
	}
}

func TestPromoteNoDuplicate(t *testing.T) {
	db := newTestDB(t)
	chat, mem := seedChatMemory(t, db)
	q := "Jangan pernah tampilkan stack trace ke user"
	for i := 0; i < 5; i++ {
		if err := Capture(db, *chat, "coding", q, "ok"); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := db.ListSkillItems(mem.ID, 100)
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 item after 5 captures, got %d", len(items))
	}
	// Non-imperative chatter never promotes.
	db2 := newTestDB(t)
	chat2, mem2 := seedChatMemory(t, db2)
	for i := 0; i < 5; i++ {
		_ = Capture(db2, *chat2, "coding", "apa kabar hari ini?", "baik")
	}
	items2, _ := db2.ListSkillItems(mem2.ID, 100)
	if len(items2) != 0 {
		t.Fatalf("expected no promotion for chatter, got %d", len(items2))
	}
}

func TestBudgetPriorityMemoryBeforeChat(t *testing.T) {
	db := newTestDB(t)
	chat := &model.Skill{ID: store.NewID("skl"), Name: "c", Description: "c",
		Type: model.SkillChat, Enabled: true, Config: `{"top_k":4,"max_chars":4000}`, CreatedAt: store.Now()}
	_ = db.CreateSkill(chat)
	_ = db.SetSkillBindings(chat.ID, true, nil)
	mem := &model.Skill{ID: store.NewID("skl"), Name: "m", Description: "m",
		Type: model.SkillMemory, Enabled: true, Config: `{"limit":20,"max_chars":4000}`, CreatedAt: store.Now()}
	_ = db.CreateSkill(mem)
	_ = db.SetSkillBindings(mem.ID, true, nil)
	// Seed one memory note and one chat exchange matching the query.
	_ = db.CreateSkillItem(&model.SkillItem{ID: store.NewID("ski"), SkillID: mem.ID,
		Kind: "note", Source: "manual", Title: "pref", Content: "selalu pakai dashboard", CreatedAt: store.Now()})
	cs, _ := db.SkillByID(chat.ID)
	_ = Capture(db, *cs, "coding", "selalu pakai dashboard baru?", "ya pakai dashboard")
	eff, _ := db.EffectiveSkills("coding")
	// Pass effective in worst order (chat first); BuildFor must reorder.
	ordered := []model.Skill{}
	for _, s := range eff {
		if s.Type == model.SkillChat {
			ordered = append([]model.Skill{s}, ordered...)
		} else {
			ordered = append(ordered, s)
		}
	}
	res, err := BuildFor(db, ordered, "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) < 2 {
		t.Fatalf("expected >=2 applied, got %d", len(res.Applied))
	}
	if res.Applied[0].Kind != model.SkillMemory {
		names := []string{}
		for _, a := range res.Applied {
			names = append(names, a.Kind+"/"+a.Skill)
		}
		t.Fatalf("memory must win budget first, got [%s]", strings.Join(names, ", "))
	}
}
