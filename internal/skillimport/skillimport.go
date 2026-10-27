// Package skillimport turns a skills.sh link (or an owner/repo[/skill]
// shorthand) into a parsed SKILL.md fetched from the public GitHub repository
// behind it.
//
// skills.sh only exposes its catalog through an API protected by Vercel OIDC
// tokens, so the importer deliberately never calls skills.sh: the URL is
// resolved to a GitHub owner/repo and SKILL.md is located through the GitHub
// trees API and downloaded from raw.githubusercontent.com.
//
// Fetch is safe to expose to an admin endpoint: user input only selects a
// validated owner/repo/skill, every request goes to a fixed GitHub host over
// https, redirects are limited, and the response size is bounded.
package skillimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// requestTimeout bounds a whole Fetch call.
	requestTimeout = 30 * time.Second
	// defaultClientTimeout bounds a single HTTP request when Client is nil.
	defaultClientTimeout = 20 * time.Second
	// maxRedirects is the maximum number of redirects a request may follow.
	maxRedirects = 5
	// maxRawBytes bounds the SKILL.md response body.
	maxRawBytes = 512 * 1024
	// maxBodyBytes bounds the imported content after frontmatter is stripped.
	maxBodyBytes = 200 * 1024
	// maxTreeBytes bounds the GitHub trees API response we decode.
	maxTreeBytes = 8 << 20
	// maxNameRunes and maxDescriptionRunes bound the parsed metadata.
	maxNameRunes        = 64
	maxDescriptionRunes = 500
	userAgent           = "MyRouter-SkillImport"
)

var (
	utf8BOM     = []byte{0xEF, 0xBB, 0xBF}
	errNotFound = errors.New("not found")
)

// Imported is one SKILL.md resolved from GitHub and parsed for import.
type Imported struct {
	Name        string
	Description string
	Content     string // SKILL.md body (frontmatter stripped), prefixed with "Source: <url>\n\n"
	SourceURL   string // canonical https://skills.sh/owner/repo/skill when derivable, else the GitHub URL
	Owner       string
	Repo        string
	Path        string // path of SKILL.md inside the repo
	Truncated   bool
}

// Fetcher resolves and downloads SKILL.md files. The zero value is usable: an
// empty GitHubAPI/RawBase falls back to the public GitHub endpoints and a nil
// Client gets a 20s-timeout client. Tests inject httptest server URLs here.
type Fetcher struct {
	Client    *http.Client
	GitHubAPI string
	RawBase   string
}

// NewFetcher returns a Fetcher pointed at the public GitHub endpoints.
func NewFetcher() *Fetcher {
	f := &Fetcher{
		GitHubAPI: defaultGitHubAPI,
		RawBase:   defaultRawBase,
	}
	f.Client = &http.Client{
		Timeout:       defaultClientTimeout,
		CheckRedirect: f.checkRedirect,
	}
	return f
}

// Fetch resolves ref to a SKILL.md on GitHub and returns its parsed content.
func (f *Fetcher) Fetch(ctx context.Context, ref string) (Imported, error) {
	owner, repo, skill, err := ParseRef(ref)
	if err != nil {
		return Imported{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	path, raw, err := f.resolve(ctx, owner, repo, skill)
	if err != nil {
		return Imported{}, err
	}

	name, description, body := parseFrontmatter(string(raw))
	body = strings.TrimSpace(body)
	body, truncated := truncateUTF8(body, maxBodyBytes)

	// The skill slug can be resolved from the reference or, for a bare repo
	// link, from the directory that holds SKILL.md.
	slug := skill
	if slug == "" {
		slug = skillSlugFromPath(path)
	}
	if description == "" {
		description = firstParagraph(body)
	}
	if name == "" {
		name = slug
	}
	if name == "" {
		name = repo
	}
	name = sanitizeName(name)
	description = sanitizeDescription(description)
	sourceURL := canonicalSourceURL(owner, repo, slug)

	return Imported{
		Name:        name,
		Description: description,
		Content:     "Source: " + sourceURL + "\n\n" + body,
		SourceURL:   sourceURL,
		Owner:       owner,
		Repo:        repo,
		Path:        path,
		Truncated:   truncated,
	}, nil
}

// resolve finds the SKILL.md path (via the trees API when possible) and
// downloads its content. The raw fallback is only used when the trees API is
// unavailable, e.g. rate limited or blocked.
func (f *Fetcher) resolve(ctx context.Context, owner, repo, skill string) (string, []byte, error) {
	paths, treeErr := f.treePaths(ctx, owner, repo)
	if treeErr == nil {
		path, err := selectSkillPath(paths, skill, owner, repo)
		if err != nil {
			return "", nil, err
		}
		raw, err := f.fetchRaw(ctx, owner, repo, path)
		if err != nil {
			return "", nil, err
		}
		return path, raw, nil
	}

	// The trees API failed; guess the conventional locations instead.
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	candidates := fallbackSkillPaths(skill)
	tried := make([]string, 0, len(candidates))
	var lastErr error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		raw, err := f.fetchRaw(ctx, owner, repo, candidate)
		if err == nil {
			return candidate, raw, nil
		}
		if errors.Is(err, errNotFound) {
			tried = append(tried, candidate)
			continue
		}
		lastErr = err
		break
	}
	if lastErr != nil {
		return "", nil, lastErr
	}
	return "", nil, fmt.Errorf(
		"skillimport: could not fetch SKILL.md for %s/%s (GitHub trees API: %v; tried: %s)",
		owner, repo, treeErr, strings.Join(tried, ", "),
	)
}

// treePaths lists non-directory paths of the repository's default branch.
func (f *Fetcher) treePaths(ctx context.Context, owner, repo string) ([]string, error) {
	endpoint := f.apiBase() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/git/trees/HEAD?recursive=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("skillimport: building GitHub trees request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("skillimport: GitHub trees request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("skillimport: GitHub trees request failed: %s", resp.Status)
	}
	var payload struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTreeBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("skillimport: decoding GitHub trees response: %w", err)
	}
	paths := make([]string, 0, len(payload.Tree))
	for _, entry := range payload.Tree {
		if entry.Type == "tree" {
			continue
		}
		paths = append(paths, entry.Path)
	}
	return paths, nil
}

// selectSkillPath picks the best SKILL.md out of a repository tree.
func selectSkillPath(paths []string, skill, owner, repo string) (string, error) {
	candidates := filterSkillPaths(paths)
	if len(candidates) == 0 {
		return "", fmt.Errorf("skillimport: no SKILL.md found in %s/%s", owner, repo)
	}
	if skill == "" {
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		sort.Strings(candidates)
		return "", ambiguousSkillPathError(owner, repo, candidates)
	}

	// Most SKILL.md files in a multi-skill repository live at
	// skills/{skill}/SKILL.md, so prefer that exact location.
	want := "skills/" + skill + "/SKILL.md"
	for _, path := range candidates {
		if strings.EqualFold(path, want) {
			return path, nil
		}
	}
	var matches []string
	for _, path := range candidates {
		if hasSkillDirSegment(path, skill) {
			matches = append(matches, path)
		}
	}
	if len(matches) > 0 {
		sort.SliceStable(matches, func(i, j int) bool {
			if len(matches[i]) != len(matches[j]) {
				return len(matches[i]) < len(matches[j])
			}
			return matches[i] < matches[j]
		})
		return matches[0], nil
	}
	sort.Strings(candidates)
	return "", fmt.Errorf(
		"skillimport: no SKILL.md for skill %q in %s/%s (candidates: %s)",
		skill, owner, repo, strings.Join(candidates, ", "),
	)
}

func filterSkillPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		normalized := strings.ToLower(strings.TrimPrefix(path, "/"))
		if normalized == "skill.md" || strings.HasSuffix(normalized, "/skill.md") {
			out = append(out, path)
		}
	}
	return out
}

func hasSkillDirSegment(path, skill string) bool {
	return strings.HasSuffix(strings.ToLower(path), "/"+strings.ToLower(skill)+"/skill.md")
}

func ambiguousSkillPathError(owner, repo string, candidates []string) error {
	shown := candidates
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return fmt.Errorf(
		"skillimport: ambiguous SKILL.md in %s/%s: %d candidates (%s)",
		owner, repo, len(candidates), strings.Join(shown, ", "),
	)
}

// fallbackSkillPaths guesses the conventional SKILL.md locations used when the
// trees API cannot be queried.
func fallbackSkillPaths(skill string) []string {
	if skill == "" {
		return []string{"SKILL.md"}
	}
	return []string{
		"SKILL.md",
		"skills/" + skill + "/SKILL.md",
		".claude/skills/" + skill + "/SKILL.md",
		skill + "/SKILL.md",
	}
}

// fetchRaw downloads one path from the repository. A 404 is reported through
// errNotFound so the fallback loop can try the next candidate.
func (f *Fetcher) fetchRaw(ctx context.Context, owner, repo, path string) ([]byte, error) {
	endpoint := f.rawBase() + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/HEAD/" + escapePath(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("skillimport: building raw request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("skillimport: fetching %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", errNotFound, path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skillimport: fetching %s: unexpected status %s", path, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRawBytes+1))
	if err != nil {
		return nil, fmt.Errorf("skillimport: reading %s: %w", path, err)
	}
	if len(data) > maxRawBytes {
		return nil, fmt.Errorf("skillimport: %s exceeds the %d KiB limit", path, maxRawBytes/1024)
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("skillimport: %s looks binary (contains a NUL byte)", path)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("skillimport: %s is empty", path)
	}
	return data, nil
}

// httpClient returns a copy of the configured client so the redirect policy is
// enforced even when a caller (or a test) injects its own *http.Client.
func (f *Fetcher) httpClient() *http.Client {
	base := f.Client
	if base == nil {
		base = &http.Client{Timeout: defaultClientTimeout}
	}
	client := *base
	client.CheckRedirect = f.checkRedirect
	return &client
}

// checkRedirect enforces the redirect policy: at most maxRedirects hops and
// every hop must stay on an allowed https host.
func (f *Fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("skillimport: refusing redirect after %d hops", maxRedirects)
	}
	if req.URL == nil || !f.redirectAllowed(req.URL) {
		target := "unknown URL"
		if req.URL != nil {
			target = req.URL.Redacted()
		}
		return fmt.Errorf("skillimport: refusing redirect to %q: not allowed", target)
	}
	return nil
}

func (f *Fetcher) redirectAllowed(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return allowedRedirectHosts[host] || f.overrideHost(host)
	case "http":
		// Only httptest overrides configured through GitHubAPI/RawBase may
		// use plain http; production always requires https.
		return f.insecureOverrideHost(host)
	default:
		return false
	}
}

// overrideHost reports whether host belongs to an injected (test) base URL.
func (f *Fetcher) overrideHost(host string) bool {
	if host == "" {
		return false
	}
	return host == hostnameOf(f.apiBase()) || host == hostnameOf(f.rawBase())
}

// insecureOverrideHost reports whether host is an injected http base host.
func (f *Fetcher) insecureOverrideHost(host string) bool {
	return (insecureBase(f.apiBase()) && host == hostnameOf(f.apiBase())) ||
		(insecureBase(f.rawBase()) && host == hostnameOf(f.rawBase()))
}

func (f *Fetcher) apiBase() string {
	base := strings.TrimRight(strings.TrimSpace(f.GitHubAPI), "/")
	if base == "" {
		return defaultGitHubAPI
	}
	return base
}

func (f *Fetcher) rawBase() string {
	base := strings.TrimRight(strings.TrimSpace(f.RawBase), "/")
	if base == "" {
		return defaultRawBase
	}
	return base
}

func canonicalSourceURL(owner, repo, slug string) string {
	if slug != "" {
		return "https://skills.sh/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/" + url.PathEscape(slug)
	}
	return "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// skillSlugFromPath derives a skill slug from the directory holding SKILL.md.
func skillSlugFromPath(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 2 {
		return ""
	}
	return segments[len(segments)-2]
}

func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}
