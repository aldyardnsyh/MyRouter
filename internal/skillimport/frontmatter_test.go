package skillimport

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantName string
		wantDesc string
		wantBody string
	}{
		{
			name:     "no frontmatter",
			in:       "Just text\n\nmore text",
			wantBody: "Just text\n\nmore text",
		},
		{
			name:     "plain scalars",
			in:       "---\nname: alpha\ndescription: Does things\n---\nBody here",
			wantName: "alpha", wantDesc: "Does things", wantBody: "Body here",
		},
		{
			name:     "quoted scalars",
			in:       "---\nname: \"alpha\"\ndescription: 'Does things'\n---\n# Body",
			wantName: "alpha", wantDesc: "Does things", wantBody: "# Body",
		},
		{
			name:     "crlf",
			in:       "---\r\nname: alpha\r\ndescription: Does things\r\n---\r\nBody here\r\n",
			wantName: "alpha", wantDesc: "Does things", wantBody: "Body here",
		},
		{
			name:     "unknown keys ignored",
			in:       "---\nname: alpha\nallowed-tools: Read, Write\nmetadata:\n  owner: x\n---\nBody",
			wantName: "alpha", wantBody: "Body",
		},
		{
			name:     "frontmatter only",
			in:       "---\nname: alpha\ndescription: desc\n---\n",
			wantName: "alpha", wantDesc: "desc",
		},
		{
			name:     "unclosed frontmatter is body",
			in:       "---\nname: alpha\nbody text",
			wantBody: "---\nname: alpha\nbody text",
		},
		{
			name:     "leading blank lines",
			in:       "\n\n---\nname: alpha\n---\nBody",
			wantName: "alpha", wantBody: "Body",
		},
		{
			name:     "empty document",
			in:       "",
			wantBody: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, desc, body := parseFrontmatter(tc.in)
			if name != tc.wantName || desc != tc.wantDesc || body != tc.wantBody {
				t.Fatalf("parseFrontmatter(%q) = (%q, %q, %q); want (%q, %q, %q)",
					tc.in, name, desc, body, tc.wantName, tc.wantDesc, tc.wantBody)
			}
		})
	}
}

func TestFirstParagraph(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   \n\n", ""},
		{"first line\nsecond line\n\nlater", "first line second line"},
		{"# Heading\n\nBody", "Heading"},
		{"\n\n## Heading with spaces\n\nBody", "Heading with spaces"},
		{"Body only", "Body only"},
	}
	for _, tc := range tests {
		if got := firstParagraph(tc.in); got != tc.want {
			t.Fatalf("firstParagraph(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("  a\x00b\x1bc\x7f  "); got != "abc" {
		t.Fatalf("sanitizeName with control chars = %q, want abc", got)
	}
	long := strings.Repeat("a", 100)
	if got := sanitizeName(long); utf8.RuneCountInString(got) != maxNameRunes {
		t.Fatalf("sanitizeName length = %d, want %d", utf8.RuneCountInString(got), maxNameRunes)
	}
	if got := sanitizeName("   "); got != "" {
		t.Fatalf("sanitizeName blank = %q", got)
	}
}

func TestSanitizeDescription(t *testing.T) {
	long := strings.Repeat("d", 700)
	if got := sanitizeDescription(long); utf8.RuneCountInString(got) != maxDescriptionRunes {
		t.Fatalf("sanitizeDescription length = %d, want %d", utf8.RuneCountInString(got), maxDescriptionRunes)
	}
	if got := sanitizeDescription("line one\nline two"); got != "line one line two" {
		t.Fatalf("sanitizeDescription newline = %q", got)
	}
}

func TestTruncateUTF8(t *testing.T) {
	// Two-byte runes: cutting at 5 bytes would split the third rune.
	in := strings.Repeat("é", 10)
	got, truncated := truncateUTF8(in, 5)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if got != "éé" {
		t.Fatalf("truncateUTF8 = %q, want %q", got, "éé")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncateUTF8 produced invalid UTF-8: %q", got)
	}
	if got, truncated := truncateUTF8("short", 100); truncated || got != "short" {
		t.Fatalf("truncateUTF8 no-op = %q, %v", got, truncated)
	}
}

func TestSkillSlugFromPath(t *testing.T) {
	tests := map[string]string{
		"skills/find-skills/SKILL.md": "find-skills",
		"SKILL.md":                    "",
		"/skills/foo/SKILL.md":        "foo",
	}
	for path, want := range tests {
		if got := skillSlugFromPath(path); got != want {
			t.Fatalf("skillSlugFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}
