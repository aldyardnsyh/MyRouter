package skillimport

import (
	"strings"
)

// Capability is the inferred behavior of a SKILL.md document.
type Capability struct {
	Type    string // prompt | memory | retrieval | chat | vision
	Summary string // 1-2 sentences
	Prompt  string // suggested config.prompt (3-5 instruction lines)
}

var visionKW = []string{"screenshot", "image", "vision", "picture", "photo", "ocr", "camera", "visual"}
var memoryKW = []string{"remember", "memory", "recall", "preference", "forget", "persist user"}
var retrievalKW = []string{"search", "retrieve", "retrieval", "docs", "documentation", "knowledge base", "lookup", "index"}
var chatKW = []string{"conversation", "chat", "dialogue", "persona", "tone", "greeting"}

// InferCapability reads SKILL.md content and infers the matching skill type,
// a 1-2 sentence summary, and a suggested prompt text.
func InferCapability(content string) Capability {
	lower := strings.ToLower(content)
	_, description, body := parseFrontmatter(content)
	if strings.TrimSpace(body) == "" {
		body = content
	}
	score := func(kws []string) int {
		n := 0
		for _, k := range kws {
			if strings.Contains(lower, k) {
				n++
			}
		}
		return n
	}
	typ := "prompt"
	switch {
	case score(visionKW) > 0:
		typ = "vision"
	case score(memoryKW) > 0:
		typ = "memory"
	case score(retrievalKW) > 0:
		typ = "retrieval"
	case score(chatKW) > 0:
		typ = "chat"
	}

	summary := buildSummary(description, body)
	prompt := extractPrompt(body)
	return Capability{Type: typ, Summary: summary, Prompt: prompt}
}

func buildSummary(description, body string) string {
	desc := strings.TrimSpace(description)
	heading := firstHeading(body)
	switch {
	case desc != "" && heading != "":
		if strings.Contains(strings.ToLower(desc), strings.ToLower(heading)) || strings.Contains(strings.ToLower(heading), strings.ToLower(desc)) {
			return truncateSentences(desc, 2)
		}
		s := desc + " " + heading
		return truncateSentences(s, 2)
	case desc != "":
		return truncateSentences(desc, 2)
	case heading != "":
		return truncateSentences(heading, 2)
	default:
		return truncateSentences(firstParagraph(body), 2)
	}
}

func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			t = strings.TrimLeft(t, "#")
			t = strings.TrimSpace(t)
			if t != "" {
				return t
			}
		}
	}
	return ""
}

func truncateSentences(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	// Split on ". " boundaries, keep periods.
	parts := strings.SplitAfter(s, ". ")
	if len(parts) <= max {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.Join(parts[:max], ""))
}

// extractPrompt returns the first 3-5 relevant instruction lines:
// imperative sentences, bullet steps, or numbered steps, skipping headers.
func extractPrompt(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "Source:") {
			continue
		}
		// Strip bullet/number prefixes for the relevance check.
		probe := strings.TrimLeft(t, "-*•> ")
		probe = strings.TrimSpace(strings.TrimLeftFunc(probe, func(r rune) bool {
			return r >= '0' && r <= '9' || r == '.' || r == ')' || r == ':'
		}))
		if len(probe) < 20 {
			continue
		}
		lower := strings.ToLower(probe)
		if startsImperative(lower) || strings.Contains(lower, "should") || strings.Contains(lower, "always") || strings.Contains(lower, "never") || strings.Contains(lower, "must") || strings.HasPrefix(strings.TrimSpace(line), "-") || strings.HasPrefix(strings.TrimSpace(line), "*") {
			out = append(out, t)
		}
		if len(out) == 5 {
			break
		}
	}
	// Fallback: first 3 non-empty body lines.
	if len(out) < 3 {
		for _, line := range strings.Split(body, "\n") {
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "Source:") {
				continue
			}
			dup := false
			for _, e := range out {
				if e == t {
					dup = true
				}
			}
			if !dup {
				out = append(out, t)
			}
			if len(out) == 3 {
				break
			}
		}
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return strings.Join(out, "\n")
}

func startsImperative(lower string) bool {
	for _, v := range []string{"use ", "do ", "don't ", "do not ", "always ", "never ", "when ", "if ", "extract ", "generate ", "create ", "analyze ", "analyse ", "summar", "search ", "remember ", "store ", "capture ", "describe ", "explain ", "answer ", "respond ", "follow ", "apply ", "check ", "ensure ", "keep ", "avoid "} {
		if strings.HasPrefix(lower, v) {
			return true
		}
	}
	return false
}
