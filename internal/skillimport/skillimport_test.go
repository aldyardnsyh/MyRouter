package skillimport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newTestFetcher(t *testing.T, handler http.HandlerFunc) *Fetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Fetcher{GitHubAPI: srv.URL, RawBase: srv.URL, Client: srv.Client()}
}

func treeResponse(paths ...string) string {
	var b strings.Builder
	b.WriteString(`{"tree":[`)
	for i, path := range paths {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"path":"` + path + `","type":"blob"}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestNewFetcherDefaults(t *testing.T) {
	f := NewFetcher()
	if f.Client == nil {
		t.Fatal("NewFetcher returned a nil Client")
	}
	if f.Client.Timeout != 20*time.Second {
		t.Fatalf("client timeout = %v, want 20s", f.Client.Timeout)
	}
	if f.Client.CheckRedirect == nil {
		t.Fatal("client has no redirect policy")
	}
	if f.GitHubAPI != "https://api.github.com" {
		t.Fatalf("GitHubAPI = %q", f.GitHubAPI)
	}
	if f.RawBase != "https://raw.githubusercontent.com" {
		t.Fatalf("RawBase = %q", f.RawBase)
	}
}

func TestParseRef(t *testing.T) {
	valid := []struct {
		name  string
		ref   string
		owner string
		repo  string
		skill string
	}{
		{"skills.sh", "https://skills.sh/vercel-labs/skills/find-skills", "vercel-labs", "skills", "find-skills"},
		{"www host", "https://www.skills.sh/vercel-labs/skills/find-skills", "vercel-labs", "skills", "find-skills"},
		{"trailing slash", "https://skills.sh/vercel-labs/skills/find-skills/", "vercel-labs", "skills", "find-skills"},
		{"query and fragment", "https://skills.sh/vercel-labs/skills/find-skills?utm=x#top", "vercel-labs", "skills", "find-skills"},
		{"padded", "  https://skills.sh/vercel-labs/skills/find-skills  ", "vercel-labs", "skills", "find-skills"},
		{"api url", "https://skills.sh/api/v1/skills/vercel-labs/skills/find-skills", "vercel-labs", "skills", "find-skills"},
		{"github repo", "https://github.com/vercel-labs/skills", "vercel-labs", "skills", ""},
		{"github repo trailing slash", "https://github.com/vercel-labs/skills/", "vercel-labs", "skills", ""},
		{"github tree", "https://github.com/vercel-labs/skills/tree/main/skills/find-skills", "vercel-labs", "skills", "find-skills"},
		{"github tree trailing slash", "https://github.com/vercel-labs/skills/tree/main/skills/find-skills/", "vercel-labs", "skills", "find-skills"},
		{"github tree branch only", "https://github.com/vercel-labs/skills/tree/main", "vercel-labs", "skills", ""},
		{"shorthand repo", "vercel-labs/skills", "vercel-labs", "skills", ""},
		{"shorthand skill", "vercel-labs/skills/find-skills", "vercel-labs", "skills", "find-skills"},
		{"shorthand padded", " Vercel-Labs/Skills/Find-Skills ", "Vercel-Labs", "Skills", "Find-Skills"},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, skill, err := ParseRef(tc.ref)
			if err != nil {
				t.Fatalf("ParseRef(%q) error: %v", tc.ref, err)
			}
			if owner != tc.owner || repo != tc.repo || skill != tc.skill {
				t.Fatalf("ParseRef(%q) = %q, %q, %q; want %q, %q, %q",
					tc.ref, owner, repo, skill, tc.owner, tc.repo, tc.skill)
			}
		})
	}

	invalid := []struct {
		name  string
		ref   string
		match string
	}{
		{"empty", "   ", "empty skill reference"},
		{"skills.sh docs", "https://skills.sh/docs/getting-started", "unsupported skills.sh"},
		{"skills.sh repo only", "https://skills.sh/vercel-labs/skills", "unsupported skills.sh"},
		{"skills.sh extra segment", "https://skills.sh/a/b/c/d", "unsupported skills.sh"},
		{"api missing part", "https://skills.sh/api/v1/skills/a/b", "unsupported skills.sh"},
		{"unknown host", "https://evil.invalid/a/b/c", "unsupported host"},
		{"http scheme", "http://skills.sh/a/b/c", "only https"},
		{"ftp scheme", "ftp://skills.sh/a/b/c", "only https"},
		{"credentials", "https://user:pass@skills.sh/a/b/c", "credentials"},
		{"github issues", "https://github.com/a/b/issues", "unsupported github.com"},
		{"github blob", "https://github.com/a/b/blob/main/skills/x/SKILL.md", "unsupported github.com"},
		{"github tree no branch", "https://github.com/a/b/tree", "tree links need a branch"},
		{"shorthand one part", "owner", "expected owner/repo"},
		{"shorthand four parts", "owner/repo/skill/extra", "expected owner/repo"},
		{"bad repo", "owner/re po", "invalid repo"},
		{"traversal skill", "owner/repo/..", "invalid skill"},
		{"encoded space", "https://skills.sh/own%20er/repo/skill", "invalid owner"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := ParseRef(tc.ref)
			if err == nil {
				t.Fatalf("ParseRef(%q) unexpectedly succeeded", tc.ref)
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("ParseRef(%q) error %q does not contain %q", tc.ref, err, tc.match)
			}
		})
	}
}

const findSkillsMD = "---\n" +
	"name: \"find-skills\"\n" +
	"description: 'Helps discover agent skills'\n" +
	"allowed-tools: Read\n" +
	"---\n" +
	"# Find Skills\n\n" +
	"Use this when the user asks for a skill.\n"

func TestFetchHappyPath(t *testing.T) {
	var (
		treeCalls int
		rawCalls  int
		userAgent string
		accept    string
		recursive string
	)
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/vercel-labs/skills/git/trees/HEAD":
			treeCalls++
			userAgent = r.Header.Get("User-Agent")
			accept = r.Header.Get("Accept")
			recursive = r.URL.Query().Get("recursive")
			io.WriteString(w, treeResponse("skills/find-skills/SKILL.md"))
		case "/vercel-labs/skills/HEAD/skills/find-skills/SKILL.md":
			rawCalls++
			io.WriteString(w, findSkillsMD)
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	got, err := f.Fetch(context.Background(), "https://skills.sh/vercel-labs/skills/find-skills")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if treeCalls != 1 || rawCalls != 1 {
		t.Fatalf("requests = tree:%d raw:%d, want 1/1", treeCalls, rawCalls)
	}
	if userAgent != "MyRouter-SkillImport" {
		t.Fatalf("User-Agent = %q", userAgent)
	}
	if accept != "application/vnd.github+json" {
		t.Fatalf("Accept = %q", accept)
	}
	if recursive != "1" {
		t.Fatalf("recursive query = %q, want 1", recursive)
	}

	wantBody := "# Find Skills\n\nUse this when the user asks for a skill."
	wantSource := "https://skills.sh/vercel-labs/skills/find-skills"
	if got.Name != "find-skills" {
		t.Fatalf("Name = %q", got.Name)
	}
	if got.Description != "Helps discover agent skills" {
		t.Fatalf("Description = %q", got.Description)
	}
	if got.Content != "Source: "+wantSource+"\n\n"+wantBody {
		t.Fatalf("Content = %q", got.Content)
	}
	if got.SourceURL != wantSource {
		t.Fatalf("SourceURL = %q", got.SourceURL)
	}
	if got.Owner != "vercel-labs" || got.Repo != "skills" {
		t.Fatalf("Owner/Repo = %q/%q", got.Owner, got.Repo)
	}
	if got.Path != "skills/find-skills/SKILL.md" {
		t.Fatalf("Path = %q", got.Path)
	}
	if got.Truncated {
		t.Fatal("Truncated = true, want false")
	}

	// A GitHub tree link must resolve to the same SKILL.md and canonical URL.
	got, err = f.Fetch(context.Background(), "https://github.com/vercel-labs/skills/tree/main/skills/find-skills")
	if err != nil {
		t.Fatalf("Fetch(github tree): %v", err)
	}
	if got.SourceURL != wantSource || got.Name != "find-skills" {
		t.Fatalf("github tree SourceURL/Name = %q/%q", got.SourceURL, got.Name)
	}
}

func TestFetchSingleSkillNoHint(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/toolbox/git/trees/HEAD":
			io.WriteString(w, treeResponse("skills/foo/SKILL.md"))
		case "/acme/toolbox/HEAD/skills/foo/SKILL.md":
			io.WriteString(w, "Great skill.\n\nMore detail here.")
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	got, err := f.Fetch(context.Background(), "https://github.com/acme/toolbox")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Name != "foo" {
		t.Fatalf("Name = %q, want foo", got.Name)
	}
	if got.Description != "Great skill." {
		t.Fatalf("Description = %q", got.Description)
	}
	if got.SourceURL != "https://skills.sh/acme/toolbox/foo" {
		t.Fatalf("SourceURL = %q", got.SourceURL)
	}
	if got.Path != "skills/foo/SKILL.md" {
		t.Fatalf("Path = %q", got.Path)
	}
	if got.Content != "Source: https://skills.sh/acme/toolbox/foo\n\nGreat skill.\n\nMore detail here." {
		t.Fatalf("Content = %q", got.Content)
	}
}

func TestFetchRootSkillFallsBackToRepo(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/toolbox/git/trees/HEAD":
			io.WriteString(w, treeResponse("SKILL.md"))
		case "/acme/toolbox/HEAD/SKILL.md":
			io.WriteString(w, "# Root Skill\n\nBody.")
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	got, err := f.Fetch(context.Background(), "acme/toolbox")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The repository name is the last-resort name fallback.
	if got.Name != "toolbox" {
		t.Fatalf("Name = %q, want toolbox", got.Name)
	}
	if got.Description != "Root Skill" {
		t.Fatalf("Description = %q", got.Description)
	}
	if got.SourceURL != "https://github.com/acme/toolbox" {
		t.Fatalf("SourceURL = %q", got.SourceURL)
	}
	if got.Path != "SKILL.md" {
		t.Fatalf("Path = %q", got.Path)
	}
}

func TestFetchAmbiguous(t *testing.T) {
	rawCalls := 0
	paths := []string{
		"skills/a/SKILL.md", "skills/b/SKILL.md", "skills/c/SKILL.md",
		"skills/d/SKILL.md", "skills/e/SKILL.md", "skills/f/SKILL.md",
		"skills/g/SKILL.md",
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/git/trees/HEAD") {
			io.WriteString(w, treeResponse(paths...))
			return
		}
		rawCalls++
		http.NotFound(w, r)
	}
	f := newTestFetcher(t, handler)

	_, err := f.Fetch(context.Background(), "https://github.com/acme/toolbox")
	if err == nil {
		t.Fatal("Fetch unexpectedly succeeded")
	}
	for _, want := range []string{"ambiguous", "7 candidates", "skills/a/SKILL.md", "skills/e/SKILL.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "skills/f/SKILL.md") {
		t.Fatalf("error lists more than 5 candidates: %q", err)
	}
	if rawCalls != 0 {
		t.Fatalf("raw calls = %d, want 0", rawCalls)
	}
}

func TestFetchUnknownSkill(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/git/trees/HEAD") {
			io.WriteString(w, treeResponse("skills/alpha/SKILL.md"))
			return
		}
		http.NotFound(w, r)
	}
	f := newTestFetcher(t, handler)

	_, err := f.Fetch(context.Background(), "acme/toolbox/beta")
	if err == nil {
		t.Fatal("Fetch unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), `no SKILL.md for skill "beta"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchPrefersConventionalPath(t *testing.T) {
	rawPath := ""
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/trees/HEAD"):
			io.WriteString(w, treeResponse(
				".claude/skills/find-skills/SKILL.md",
				"skills/find-skills/SKILL.md",
			))
		case strings.Contains(r.URL.Path, "/HEAD/"):
			rawPath = strings.TrimPrefix(r.URL.Path, "/acme/toolbox/HEAD/")
			io.WriteString(w, "Body.")
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	got, err := f.Fetch(context.Background(), "acme/toolbox/find-skills")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if rawPath != "skills/find-skills/SKILL.md" || got.Path != "skills/find-skills/SKILL.md" {
		t.Fatalf("selected path = %q, want skills/find-skills/SKILL.md", rawPath)
	}
}

func TestFetchTreeFallback(t *testing.T) {
	var rawRequests []string
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/trees/HEAD"):
			http.Error(w, "rate limited", http.StatusForbidden)
		case strings.HasPrefix(r.URL.Path, "/acme/toolbox/HEAD/"):
			path := strings.TrimPrefix(r.URL.Path, "/acme/toolbox/HEAD/")
			rawRequests = append(rawRequests, path)
			if path == ".claude/skills/foo/SKILL.md" {
				io.WriteString(w, "Fallback body.")
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	got, err := f.Fetch(context.Background(), "acme/toolbox/foo")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	wantRequests := []string{"SKILL.md", "skills/foo/SKILL.md", ".claude/skills/foo/SKILL.md"}
	if strings.Join(rawRequests, ",") != strings.Join(wantRequests, ",") {
		t.Fatalf("raw requests = %v, want %v", rawRequests, wantRequests)
	}
	if got.Path != ".claude/skills/foo/SKILL.md" {
		t.Fatalf("Path = %q", got.Path)
	}
	if got.Name != "foo" || got.Description != "Fallback body." {
		t.Fatalf("Name/Description = %q/%q", got.Name, got.Description)
	}
}

func TestFetchFallbackAllMissing(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/git/trees/HEAD") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}
	f := newTestFetcher(t, handler)

	_, err := f.Fetch(context.Background(), "acme/toolbox/foo")
	if err == nil {
		t.Fatal("Fetch unexpectedly succeeded")
	}
	for _, want := range []string{"could not fetch SKILL.md", "tried:", "skills/foo/SKILL.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestFetchNoSkillMD(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/git/trees/HEAD") {
			io.WriteString(w, treeResponse("README.md", "docs/guide.md"))
			return
		}
		http.NotFound(w, r)
	}
	f := newTestFetcher(t, handler)

	_, err := f.Fetch(context.Background(), "acme/toolbox/foo")
	if err == nil || !strings.Contains(err.Error(), "no SKILL.md found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchRejectsUnsafeRef(t *testing.T) {
	requests := 0
	f := newTestFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	})

	for _, ref := range []string{
		"http://evil.invalid/a/b/c",
		"https://evil.invalid/a/b/c",
	} {
		if _, err := f.Fetch(context.Background(), ref); err == nil {
			t.Fatalf("Fetch(%q) unexpectedly succeeded", ref)
		}
	}
	if requests != 0 {
		t.Fatalf("unsafe refs made %d HTTP requests, want 0", requests)
	}
}

func TestFetchRejectsUnsafeRedirect(t *testing.T) {
	f := newTestFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.invalid/SKILL.md", http.StatusFound)
	})

	_, err := f.Fetch(context.Background(), "acme/toolbox/foo")
	if err == nil {
		t.Fatal("Fetch unexpectedly followed a redirect to an unsafe host")
	}
	if !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchTruncation(t *testing.T) {
	body := strings.Repeat("x", maxBodyBytes+10*1024)
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/trees/HEAD"):
			io.WriteString(w, treeResponse("skills/foo/SKILL.md"))
		case strings.HasPrefix(r.URL.Path, "/acme/toolbox/HEAD/"):
			io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	got, err := f.Fetch(context.Background(), "acme/toolbox/foo")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	prefix := "Source: https://skills.sh/acme/toolbox/foo\n\n"
	if !strings.HasPrefix(got.Content, prefix) {
		t.Fatalf("Content does not start with %q", prefix)
	}
	importedBody := strings.TrimPrefix(got.Content, prefix)
	if len(importedBody) != maxBodyBytes {
		t.Fatalf("body length = %d, want %d", len(importedBody), maxBodyBytes)
	}
	if !utf8.ValidString(got.Content) {
		t.Fatal("Content is not valid UTF-8")
	}
}

func TestFetchRejectsOversize(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/trees/HEAD"):
			io.WriteString(w, treeResponse("skills/foo/SKILL.md"))
		case strings.HasPrefix(r.URL.Path, "/acme/toolbox/HEAD/"):
			io.WriteString(w, strings.Repeat("x", maxRawBytes+1))
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	_, err := f.Fetch(context.Background(), "acme/toolbox/foo")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchRejectsBinary(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/trees/HEAD"):
			io.WriteString(w, treeResponse("skills/foo/SKILL.md"))
		case strings.HasPrefix(r.URL.Path, "/acme/toolbox/HEAD/"):
			io.WriteString(w, "hello\x00world")
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	_, err := f.Fetch(context.Background(), "acme/toolbox/foo")
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchStripsBOM(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/git/trees/HEAD"):
			io.WriteString(w, treeResponse("skills/foo/SKILL.md"))
		case strings.HasPrefix(r.URL.Path, "/acme/toolbox/HEAD/"):
			io.WriteString(w, "\xEF\xBB\xBF---\nname: foo\ndescription: bom body\n---\nBody.")
		default:
			http.NotFound(w, r)
		}
	}
	f := newTestFetcher(t, handler)

	got, err := f.Fetch(context.Background(), "acme/toolbox/foo")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Name != "foo" || got.Description != "bom body" || !strings.HasSuffix(got.Content, "\n\nBody.") {
		t.Fatalf("imported = %+v", got)
	}
}

func TestFetchContextCanceled(t *testing.T) {
	f := newTestFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.Fetch(ctx, "acme/toolbox/foo")
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("unexpected error: %v", err)
	}
}
