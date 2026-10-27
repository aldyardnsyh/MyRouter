package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createSkill(t *testing.T, db *store.DB, name, typ string, cfg map[string]any) *model.Skill {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	sk := &model.Skill{
		ID: store.NewID("skl"), Name: name, Description: name, Type: typ,
		Enabled: true, Config: string(raw), CreatedAt: store.Now(),
	}
	if err := db.CreateSkill(sk); err != nil {
		t.Fatalf("create skill: %v", err)
	}
	if err := db.SetSkillBindings(sk.ID, false, []string{"coding"}); err != nil {
		t.Fatalf("bind skill: %v", err)
	}
	saved, err := db.SkillByID(sk.ID)
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	return saved
}

func TestSeedBuiltinSkillsBindsGlobally(t *testing.T) {
	db := newTestDB(t)
	if err := db.SeedBuiltinSkills(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	list, err := db.ListSkills()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 6 {
		t.Fatalf("expected 6 builtins, got %d", len(list))
	}
	seen := map[string]bool{}
	for _, sk := range list {
		seen[sk.Name] = true
		if !sk.Bindings.Global {
			t.Errorf("builtin %s is not bound globally", sk.Name)
		}
	}
	for _, want := range []string{"xerj", "ai-memory", "chat-knowledge", "vision", "memory", "core"} {
		if !seen[want] {
			t.Errorf("missing builtin %s", want)
		}
	}

	// A route created later automatically inherits the global skills.
	effective, err := db.EffectiveSkills("brand-new-route")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if len(effective) == 0 {
		t.Fatal("expected global skills to apply to a new route")
	}

	// Re-seeding is idempotent and never duplicates.
	if err := db.SeedBuiltinSkills(); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	again, _ := db.ListSkills()
	if len(again) != len(list) {
		t.Fatalf("reseed duplicated skills: %d -> %d", len(list), len(again))
	}
}

func TestBuildForPromptMemoryRetrieval(t *testing.T) {
	db := newTestDB(t)
	prompt := createSkill(t, db, "core", model.SkillPrompt, map[string]any{
		"prompt": "Always answer in Indonesian.", "max_chars": 500,
	})
	memory := createSkill(t, db, "notes", model.SkillMemory, map[string]any{"limit": 5, "max_chars": 1000})
	for _, note := range []string{"deploy uses Docker", "db is sqlite"} {
		_ = db.CreateSkillItem(&model.SkillItem{
			ID: store.NewID("ski"), SkillID: memory.ID, Kind: "note", Source: "manual",
			Title: note, Content: note, CreatedAt: store.Now(),
		})
	}
	retrieval := createSkill(t, db, "code", model.SkillRetrieval, map[string]any{
		"top_k": 2, "max_chars": 1000, "sync_glob": "*.go",
	})
	for _, tc := range []struct{ title, body string }{
		{"router.go", "func handleChat routes chat completions"},
		{"skills.go", "skill retrieval ranks passages by query terms"},
		{"unrelated.go", "database migrations and schema"},
	} {
		_ = db.CreateSkillItem(&model.SkillItem{
			ID: store.NewID("ski"), SkillID: retrieval.ID, Kind: "doc", Source: tc.title,
			Title: tc.title, Content: tc.body, CreatedAt: store.Now(),
		})
	}

	effective, err := db.EffectiveSkills("coding")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	res, err := BuildFor(db, effective, "how does the router handle skill retrieval?")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(res.Context, "Always answer in Indonesian.") {
		t.Error("prompt skill missing from context")
	}
	if !strings.Contains(res.Context, "deploy uses Docker") {
		t.Error("memory skill missing from context")
	}
	if !strings.Contains(res.Context, "router.go") {
		t.Error("retrieval skill missing from context")
	}
	if strings.Contains(res.Context, "database migrations") {
		t.Error("unrelated passage should not be injected for this query")
	}
	if len(res.Applied) != 3 {
		t.Errorf("expected 3 applied skills, got %d (%+v)", len(res.Applied), res.Applied)
	}
	if prompt.ID == "" || res.Context == "" {
		t.Fatal("sanity check")
	}

	// A route with no binding gets nothing.
	other, _ := BuildFor(db, nil, "anything")
	if other.Context != "" {
		t.Error("expected empty context for empty skill list")
	}
}

func TestCaptureRankAndTrim(t *testing.T) {
	db := newTestDB(t)
	chat := createSkill(t, db, "chat-knowledge", model.SkillChat, map[string]any{
		"top_k": 3, "max_chars": 2000, "capture_max_chars": 500, "capture_keep": 2,
	})
	_ = Capture(db, *chat, "coding", "What database does this use?", "SQLite with WAL enabled.")
	_ = Capture(db, *chat, "coding", "Tertiary exchange", "Third answer.")
	if err := Capture(db, *chat, "coding", "How do I deploy with Docker?", "Use docker compose and the master key env var."); err != nil {
		t.Fatalf("capture: %v", err)
	}
	items, _ := db.ListSkillItems(chat.ID, 50)
	if len(items) != 2 {
		t.Fatalf("capture_keep=2 should trim to 2 items, got %d", len(items))
	}
	res := Preview(db, *chat, "Docker deploy")
	if !strings.Contains(res.Context, "past exchanges") {
		t.Errorf("chat skill should retrieve past exchanges, got %q", res.Context)
	}
	if !strings.Contains(res.Context, "How do I deploy with Docker?") {
		t.Errorf("expected the Docker exchange, got %q", res.Context)
	}
}

func TestSyncSkillIngestsAndReplaces(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# Project\nRouter documentation.")
	write("pkg/notes.md", "Design notes for the router.")
	write("pkg/code.go", "package pkg")

	sk := createSkill(t, db, "wiki", model.SkillRetrieval, map[string]any{
		"top_k": 4, "sync_paths": []string{dir}, "sync_glob": "*.md",
	})
	report := SyncSkill(db, *sk)
	if report.Error != "" {
		t.Fatalf("sync error: %s", report.Error)
	}
	if report.Files != 2 || report.Items != 2 {
		t.Fatalf("expected 2 files/2 items, got files=%d items=%d", report.Files, report.Items)
	}
	items, _ := db.ListSkillItems(sk.ID, 50)
	if len(items) != 2 {
		t.Fatalf("expected 2 stored items, got %d", len(items))
	}
	if _, _, _, err := db.SkillState(sk.ID); err != nil {
		t.Fatalf("state: %v", err)
	}

	// Re-sync replaces the derived docs instead of piling up duplicates.
	_ = os.Remove(filepath.Join(dir, "README.md"))
	report = SyncSkill(db, *sk)
	if report.Items != 1 {
		t.Fatalf("expected 1 item after deletion, got %d", report.Items)
	}
	items, _ = db.ListSkillItems(sk.ID, 50)
	if len(items) != 1 {
		t.Fatalf("expected 1 stored item after replace, got %d", len(items))
	}
}

func TestSyncSkillWithoutPaths(t *testing.T) {
	db := newTestDB(t)
	sk := createSkill(t, db, "empty", model.SkillRetrieval, map[string]any{})
	report := SyncSkill(db, *sk)
	if report.Error == "" {
		t.Fatal("expected an error when no sync paths are configured")
	}
}

func TestGlobHelpers(t *testing.T) {
	if !matchGlob("*.{go,ts}", "router.go") || !matchGlob("*.{go,ts}", "app.ts") {
		t.Error("brace expansion failed")
	}
	if matchGlob("*.{go,ts}", "README.md") {
		t.Error("glob matched an unexpected extension")
	}
	if !matchGlob("", "anything.txt") {
		t.Error("empty glob should match everything")
	}
	if got := expandBraces("a{1,2}b"); len(got) != 2 || got[0] != "a1b" || got[1] != "a2b" {
		t.Errorf("unexpected brace expansion: %v", got)
	}
}

func TestChunkText(t *testing.T) {
	long := strings.Repeat("x", 25)
	chunks := chunkText(long, 10, 2)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if len(chunks[0]) != 10 || len(chunks[2]) != 9 {
		t.Errorf("unexpected chunk sizes: %d/%d", len(chunks[0]), len(chunks[2]))
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg := ParseConfig(`{"top_k":2}`)
	if cfg.MaxChars != defaultMaxChars || cfg.TopK != 2 || cfg.MaxImages != 4 || cfg.CaptureKeep != 1000 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}
