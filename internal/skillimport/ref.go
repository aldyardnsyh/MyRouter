package skillimport

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Default GitHub endpoints. The importer never calls skills.sh itself: the
// skills.sh catalog API requires a Vercel OIDC token, so a skills.sh URL is
// translated into the public GitHub repository it names instead.
const (
	defaultGitHubAPI = "https://api.github.com"
	defaultRawBase   = "https://raw.githubusercontent.com"
)

var refPartRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// directRefHosts are the only hosts a user-supplied reference may name.
var directRefHosts = map[string]bool{
	"skills.sh":     true,
	"www.skills.sh": true,
	"github.com":    true,
}

// allowedRedirectHosts are the hosts GitHub/raw responses may redirect to.
// Redirect targets can never introduce a new host: everything here is either
// skills.sh or a GitHub-owned host.
var allowedRedirectHosts = map[string]bool{
	"skills.sh":                 true,
	"www.skills.sh":             true,
	"github.com":                true,
	"api.github.com":            true,
	"raw.githubusercontent.com": true,
}

// ParseRef normalizes a skills.sh link, a GitHub link, or an owner/repo[/skill]
// shorthand into the repository owner, repository name and optional skill slug.
//
// Accepted forms:
//
//	https://skills.sh/{owner}/{repo}/{skill}
//	https://www.skills.sh/{owner}/{repo}/{skill}
//	https://skills.sh/api/v1/skills/{owner}/{repo}/{skill}
//	https://github.com/{owner}/{repo}
//	https://github.com/{owner}/{repo}/tree/{branch}/{path...}
//	owner/repo
//	owner/repo/skill
//
// Trailing slashes, query strings and fragments are ignored. An empty skill
// means "resolve the repository's only SKILL.md". The skill slug is the last
// path segment of a GitHub tree link.
func ParseRef(ref string) (owner, repo, skill string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", "", fmt.Errorf("skillimport: empty skill reference")
	}
	if strings.Contains(ref, "://") {
		return parseURLRef(ref)
	}
	parts := strings.Split(strings.Trim(ref, "/"), "/")
	switch len(parts) {
	case 2:
		owner, repo = parts[0], parts[1]
	case 3:
		owner, repo, skill = parts[0], parts[1], parts[2]
	default:
		return "", "", "", fmt.Errorf("skillimport: invalid skill reference %q: expected owner/repo or owner/repo/skill", ref)
	}
	if err := validateRefParts(owner, repo, skill); err != nil {
		return "", "", "", err
	}
	return owner, repo, skill, nil
}

func parseURLRef(ref string) (string, string, string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", "", "", fmt.Errorf("skillimport: invalid skill URL %q: %w", ref, err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", "", "", fmt.Errorf("skillimport: only https skill URLs are allowed: %q", ref)
	}
	if u.User != nil {
		return "", "", "", fmt.Errorf("skillimport: credentials in skill URLs are not allowed: %q", ref)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", "", "", fmt.Errorf("skillimport: invalid skill URL %q: missing host", ref)
	}
	if !directRefHosts[host] {
		return "", "", "", fmt.Errorf("skillimport: unsupported host %q: allowed hosts are skills.sh, www.skills.sh and github.com", host)
	}
	segments := splitRefPath(u.Path)
	if host == "github.com" {
		return parseGitHubPath(segments, ref)
	}
	return parseSkillsShPath(segments, ref)
}

func splitRefPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func parseSkillsShPath(segments []string, ref string) (string, string, string, error) {
	var owner, repo, skill string
	switch {
	case len(segments) == 6 &&
		strings.EqualFold(segments[0], "api") &&
		strings.EqualFold(segments[1], "v1") &&
		strings.EqualFold(segments[2], "skills"):
		owner, repo, skill = segments[3], segments[4], segments[5]
	case len(segments) == 3:
		owner, repo, skill = segments[0], segments[1], segments[2]
	default:
		return "", "", "", fmt.Errorf("skillimport: unsupported skills.sh URL %q: expected https://skills.sh/{owner}/{repo}/{skill}", ref)
	}
	if err := validateRefParts(owner, repo, skill); err != nil {
		return "", "", "", err
	}
	return owner, repo, skill, nil
}

func parseGitHubPath(segments []string, ref string) (string, string, string, error) {
	if len(segments) < 2 {
		return "", "", "", fmt.Errorf("skillimport: unsupported github.com URL %q: expected https://github.com/{owner}/{repo} or https://github.com/{owner}/{repo}/tree/{branch}/{path}", ref)
	}
	owner, repo := segments[0], segments[1]
	var skill string
	switch {
	case len(segments) == 2:
		// Repository root: the skill is resolved from the tree.
	case segments[2] == "tree":
		if len(segments) < 4 {
			return "", "", "", fmt.Errorf("skillimport: unsupported github.com URL %q: tree links need a branch", ref)
		}
		if len(segments) > 4 {
			skill = segments[len(segments)-1]
		}
	default:
		return "", "", "", fmt.Errorf("skillimport: unsupported github.com URL %q: expected https://github.com/{owner}/{repo} or https://github.com/{owner}/{repo}/tree/{branch}/{path}", ref)
	}
	if err := validateRefParts(owner, repo, skill); err != nil {
		return "", "", "", err
	}
	return owner, repo, skill, nil
}

func validateRefParts(owner, repo, skill string) error {
	if err := validateRefPart("owner", owner); err != nil {
		return err
	}
	if err := validateRefPart("repo", repo); err != nil {
		return err
	}
	if skill != "" {
		if err := validateRefPart("skill", skill); err != nil {
			return err
		}
	}
	return nil
}

func validateRefPart(kind, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("skillimport: empty %s", kind)
	case value == "." || value == "..":
		return fmt.Errorf("skillimport: invalid %s %q", kind, value)
	case !refPartRe.MatchString(value):
		return fmt.Errorf("skillimport: invalid %s %q: must match %s", kind, value, refPartRe.String())
	}
	return nil
}

// hostnameOf returns the lowercased hostname of rawURL, or "" when unparsable.
func hostnameOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// insecureBase reports whether rawURL is an http:// base. Only test overrides
// (httptest servers injected through GitHubAPI/RawBase) use http.
func insecureBase(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "http")
}
