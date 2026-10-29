package skills

import (
	"strings"
	"unicode"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// imperativeMarkers are Indonesian/English instruction cues that signal a
// reusable user preference worth promoting to long-term memory.
var imperativeMarkers = []string{
	"selalu", "jangan", "pakai", "gunakan", "wajib", "harus",
	"always", "never", "don't", "do not", "use ", "prefer ",
}

// promoteThreshold is how many times the same preference pattern must appear
// in recent captures before it is promoted to memory.
const promoteThreshold = 3

// promoteWindowDays bounds the capture scan for pattern detection.
const promoteWindowDays = 7

// maybePromoteToMemory scans recent captures of the chat skill for a repeated
// imperative preference (>=3x in 7 days for the same route) and copies it as
// a permanent memory item into the first memory skill bound to the same route
// (global fallback). It never duplicates (similarity check) and tags the item
// source as "promoted from chat".
func maybePromoteToMemory(db *store.DB, chatSkill model.Skill, routeAlias, question string) {
	pattern := extractPreference(question)
	if pattern == "" {
		return
	}
	recent, err := recentCaptures(db, chatSkill.ID, routeAlias)
	if err != nil || len(recent) == 0 {
		return
	}
	count := 0
	for _, c := range recent {
		if similarPreference(pattern, extractPreference(c)) {
			count++
		}
	}
	if count < promoteThreshold {
		return
	}
	mem := firstMemorySkill(db, routeAlias)
	if mem == nil {
		return
	}
	existing, err := db.ListSkillItems(mem.ID, 5000)
	if err != nil {
		return
	}
	for _, it := range existing {
		if similarPreference(pattern, extractPreference(it.Content)) {
			return // already promoted
		}
	}
	_ = db.CreateSkillItem(&model.SkillItem{
		ID:        store.NewID("ski"),
		SkillID:   mem.ID,
		Kind:      "note",
		Source:    "promoted from chat",
		Title:     truncate(pattern, 120),
		Content:   pattern,
		CreatedAt: store.Now(),
	})
}

// extractPreference returns the question text when it looks like an
// imperative/instructive preference, else "".
func extractPreference(q string) string {
	lower := strings.ToLower(strings.TrimSpace(q))
	if lower == "" {
		return ""
	}
	for _, m := range imperativeMarkers {
		if strings.Contains(lower, m) {
			// Keep the first non-empty line as the canonical pattern.
			return firstLine(strings.TrimSpace(q), 300)
		}
	}
	return ""
}

// similarPreference reports whether two preference strings are near-duplicates:
// same normalized core after stripping punctuation/case, or one contains the
// other when the shorter side is substantial (>=12 runes).
func similarPreference(a, b string) bool {
	na, nb := normalizePref(a), normalizePref(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	if len([]rune(na)) >= 12 && strings.Contains(nb, na) {
		return true
	}
	if len([]rune(nb)) >= 12 && strings.Contains(na, nb) {
		return true
	}
	return false
}

func normalizePref(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// recentCaptures returns question texts captured in the last 7 days for the
// same route (source "chat:<route>"), newest first.
func recentCaptures(db *store.DB, chatSkillID, routeAlias string) ([]string, error) {
	items, err := db.ListSkillItems(chatSkillID, 1000)
	if err != nil {
		return nil, err
	}
	want := "chat:" + strings.ToLower(strings.TrimSpace(routeAlias))
	cutoff := store.Now() - int64(promoteWindowDays*24*3600*1000)
	out := []string{}
	for _, it := range items {
		if it.CreatedAt < cutoff {
			continue
		}
		if strings.ToLower(strings.TrimSpace(it.Source)) != want {
			continue
		}
		// Content is "Q: ...\n\nA: ..."; extract the Q part.
		q := it.Content
		if idx := strings.Index(q, "\n\nA:"); idx >= 0 {
			q = strings.TrimPrefix(q[:idx], "Q: ")
		}
		out = append(out, q)
	}
	return out, nil
}

// firstMemorySkill returns the first enabled memory skill bound to the route
// (route-specific first, then global fallback), or nil.
func firstMemorySkill(db *store.DB, routeAlias string) *model.Skill {
	eff, err := db.EffectiveSkills(routeAlias)
	if err != nil {
		return nil
	}
	// Prefer route-bound over global: check bindings explicitly.
	var globalFallback *model.Skill
	for i := range eff {
		if eff[i].Type != model.SkillMemory || !eff[i].Enabled {
			continue
		}
		b := db.SkillBindingsFor(eff[i].ID)
		alias := strings.ToLower(strings.TrimSpace(routeAlias))
		for _, r := range b.Routes {
			if strings.ToLower(strings.TrimSpace(r)) == alias {
				s := eff[i]
				return &s
			}
		}
		if b.Global && globalFallback == nil {
			s := eff[i]
			globalFallback = &s
		}
	}
	return globalFallback
}
