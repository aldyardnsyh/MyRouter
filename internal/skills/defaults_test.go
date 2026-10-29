package skills

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

func TestDefaultContentForType(t *testing.T) {
	for _, typ := range model.SkillTypes {
		patch, items := DefaultContentForType(typ, "AsistenDeploy")
		switch typ {
		case model.SkillPrompt:
			p, _ := patch["prompt"].(string)
			if !strings.Contains(p, "AsistenDeploy") {
				t.Errorf("prompt %s: missing role name", typ)
			}
		case model.SkillMemory:
			if len(items) != 3 {
				t.Errorf("memory: want 3 starter notes, got %d", len(items))
			}
		case model.SkillRetrieval:
			if len(items) != 2 {
				t.Errorf("retrieval: want 2 starter docs, got %d", len(items))
			}
			if _, ok := patch["sync_paths"]; !ok {
				t.Errorf("retrieval: missing sync_paths default")
			}
		case model.SkillChat:
			if len(items) == 0 {
				t.Errorf("chat: want starter exchange")
			}
		case model.SkillVision:
			vp, _ := patch["vision_prompt"].(string)
			if vp == "" {
				t.Errorf("vision: missing vision_prompt")
			}
		}
	}
}

func TestSeedDefaultsPerType(t *testing.T) {
	for _, typ := range model.SkillTypes {
		db, err := store.Open(filepath.Join(t.TempDir(), typ+".db"))
		if err != nil {
			t.Fatal(err)
		}
		sk := &model.Skill{ID: store.NewID("skl"), Name: "S" + typ, Type: typ, Enabled: true, Config: "{}"}
		if err := db.CreateSkill(sk); err != nil {
			t.Fatal(err)
		}
		if err := SeedDefaults(db, sk); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		saved, _ := db.SkillByID(sk.ID)
		cfg := ParseConfig(saved.Config)
		items, _ := db.ListSkillItems(sk.ID, 10)
		switch typ {
		case model.SkillPrompt:
			if cfg.Prompt == "" {
				t.Errorf("prompt: empty after seed")
			}
		case model.SkillMemory, model.SkillRetrieval, model.SkillChat:
			if len(items) == 0 {
				t.Errorf("%s: no starter items", typ)
			}
		case model.SkillVision:
			if cfg.VisionPrompt == "" {
				t.Errorf("vision: empty after seed")
			}
		}
		// Second seed must not duplicate.
		before := len(items)
		if err := SeedDefaults(db, sk); err != nil {
			t.Fatal(err)
		}
		after, _ := db.ListSkillItems(sk.ID, 10)
		if len(after) != before {
			t.Errorf("%s: re-seed duplicated items", typ)
		}
		db.Close()
	}
}

func TestSeedDefaultsNoOverwrite(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "custom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sk := &model.Skill{ID: store.NewID("skl"), Name: "Custom", Type: model.SkillPrompt, Enabled: true, Config: `{"prompt":"custom"}`}
	if err := db.CreateSkill(sk); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(db, sk); err != nil {
		t.Fatal(err)
	}
	if got := ParseConfig(sk.Config).Prompt; got != "custom" {
		t.Errorf("overwrote user prompt: %q", got)
	}
}
