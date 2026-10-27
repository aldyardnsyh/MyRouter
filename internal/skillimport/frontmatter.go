package skillimport

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// parseFrontmatter separates a leading YAML frontmatter block from the body.
//
// The parser is intentionally minimal because MyRouter must stay stdlib-only:
// it only understands the delimiters ("---" lines), flat single-line scalars
// and the name/description keys SKILL.md files use. Everything else in the
// frontmatter is ignored but still stripped from the body.
func parseFrontmatter(content string) (name, description, body string) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start < len(lines) && strings.TrimSpace(lines[start]) == "---" {
		end := start + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "---" {
			end++
		}
		if end < len(lines) { // closing delimiter found: valid frontmatter
			name, description = parseFrontmatterLines(lines[start+1 : end])
			return name, description, strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
		}
	}
	// No valid frontmatter block: the whole document is the body.
	return "", "", strings.TrimSpace(content)
}

func parseFrontmatterLines(lines []string) (name, description string) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = unquoteScalar(strings.TrimSpace(value))
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			if name == "" {
				name = value
			}
		case "description":
			if description == "" {
				description = value
			}
		}
	}
	return name, description
}

// unquoteScalar removes one layer of surrounding single or double quotes.
func unquoteScalar(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// firstParagraph returns the first non-empty paragraph of the body as a single
// line. Markdown heading markers are stripped so a document that starts with
// "# Name" still yields a readable description.
func firstParagraph(body string) string {
	var (
		block   []string
		started bool
	)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if started {
				break
			}
			continue
		}
		started = true
		block = append(block, trimmed)
	}
	if len(block) == 0 {
		return ""
	}
	out := strings.Join(block, " ")
	out = strings.TrimLeft(out, "#")
	return strings.TrimSpace(out)
}

// sanitizeName keeps a short, single-line, control-character-free skill name.
func sanitizeName(name string) string {
	name = stripControl(strings.TrimSpace(name))
	if utf8.RuneCountInString(name) > maxNameRunes {
		name = truncateRunes(name, maxNameRunes)
	}
	return strings.TrimSpace(name)
}

// sanitizeDescription keeps a bounded, single-line skill description.
func sanitizeDescription(description string) string {
	description = stripControl(strings.Join(strings.Fields(description), " "))
	if utf8.RuneCountInString(description) > maxDescriptionRunes {
		description = truncateRunes(description, maxDescriptionRunes)
	}
	return strings.TrimSpace(description)
}

func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// truncateUTF8 cuts s to at most maxBytes bytes without splitting a UTF-8
// rune, reporting whether anything was removed.
func truncateUTF8(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
