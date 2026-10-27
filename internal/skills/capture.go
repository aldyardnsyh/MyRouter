package skills

import (
	"strings"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// Capture stores one completed question/answer exchange as a note of a chat
// skill. The gateway calls it after a successful response. Retention is bounded
// by the skill's capture_keep so a long-running router cannot grow forever.
func Capture(db *store.DB, s model.Skill, routeAlias, question, answer string) error {
	cfg := ParseConfig(s.Config)
	question = strings.TrimSpace(question)
	answer = strings.TrimSpace(answer)
	if question == "" || answer == "" {
		return nil
	}
	question = truncate(question, cfg.CaptureMaxChars)
	answer = truncate(answer, cfg.CaptureMaxChars)
	title := firstLine(question, 120)
	if title == "" {
		title = "exchange"
	}
	content := "Q: " + question + "\n\nA: " + answer
	item := &model.SkillItem{
		ID:        store.NewID("ski"),
		SkillID:   s.ID,
		Kind:      "note",
		Source:    "chat:" + strings.ToLower(strings.TrimSpace(routeAlias)),
		Title:     title,
		Content:   content,
		CreatedAt: store.Now(),
	}
	if err := db.CreateSkillItem(item); err != nil {
		return err
	}
	return db.TrimSkillItems(s.ID, "note", cfg.CaptureKeep)
}

// firstLine returns the first non-empty line of s, truncated to max runes.
func firstLine(s string, max int) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return truncate(line, max)
		}
	}
	return ""
}
