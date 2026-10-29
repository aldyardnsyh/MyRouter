package skills

import (
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
)

func bmItem(id, title, body string) model.SkillItem {
	return model.SkillItem{ID: id, SkillID: "s", Kind: "note", Source: "manual", Title: title, Content: body}
}

func TestBM25RareTermWins(t *testing.T) {
	items := []model.SkillItem{
		bmItem("common", "notes", "docker docker docker deploy guide"),
		bmItem("rare", "notes", "zebrafish migration notes"),
	}
	got := RankItems(items, []string{"zebrafish"}, 2)
	if len(got) == 0 || got[0].ID != "rare" {
		t.Fatalf("rare term should win, got %+v", got)
	}
}

func TestBM25ShortDenseWins(t *testing.T) {
	items := []model.SkillItem{
		bmItem("long", "notes", "router "+strings.Repeat("filler words here ", 200)),
		bmItem("short", "notes", "router config"),
	}
	got := RankItems(items, []string{"router"}, 2)
	if len(got) == 0 || got[0].ID != "short" {
		t.Fatalf("short dense doc should win, got %+v", got)
	}
}

func TestBM25TFSaturation(t *testing.T) {
	docs := []model.SkillItem{
		bmItem("a", "notes", "docker uniqueword"),
		bmItem("b", "notes", strings.Repeat("docker ", 20)+"uniqueword"),
	}
	got := RankItems(docs, []string{"docker"}, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].ID != "b" {
		t.Fatalf("20x doc should still rank first, got %s", got[0].ID)
	}
	if got[1].ID != "a" {
		t.Fatalf("1x doc should still rank, got %s", got[1].ID)
	}
}
