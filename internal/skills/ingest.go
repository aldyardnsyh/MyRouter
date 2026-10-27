package skills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

const (
	maxSyncFiles = 1200
	maxSyncItems = 2000
	maxFileBytes = 512 * 1024
	chunkRunes   = 6000
	chunkOverlap = 400
)

// syncMu serializes ingest runs so a background sync and an admin "Sync now"
// cannot interleave their clear+insert sequence.
var syncMu sync.Mutex

// Directories that never carry useful reference material and can be huge.
var skipDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true, "node_modules": true, "vendor": true,
	"dist": true, "build": true, "target": true, "bin": true, "obj": true,
	"__pycache__": true, ".venv": true, "venv": true, "env": true, ".next": true,
	".cache": true, "coverage": true, ".idea": true, ".vscode": true, "venv3": true,
}

// SyncReport is the result of one ingest run.
type SyncReport struct {
	Files   int    `json:"files"`
	Items   int    `json:"items"`
	Skipped int    `json:"skipped"`
	Error   string `json:"error"`
}

// SyncSkill ingests every file selected by the skill's sync_paths/sync_glob into
// skill_item rows of kind "doc", replacing the previous generated docs. It is
// used both by the admin "Sync" button and by the periodic background syncer.
func SyncSkill(db *store.DB, s model.Skill) SyncReport {
	syncMu.Lock()
	defer syncMu.Unlock()
	cfg := ParseConfig(s.Config)
	report := SyncReport{}
	paths := cleanSyncPaths(cfg.SyncPaths)
	if len(paths) == 0 {
		report.Error = "no sync_paths configured"
		_ = db.SetSkillState(s.ID, store.Now(), report.Error, 0)
		return report
	}
	seen := map[string]bool{}
	items := make([]model.SkillItem, 0, 128)
	truncated := false
	now := store.Now()

	for _, root := range paths {
		if len(items) >= maxSyncItems || report.Files >= maxSyncFiles {
			truncated = true
			break
		}
		info, err := os.Stat(root)
		if err != nil {
			report.Skipped++
			continue
		}
		if !info.IsDir() {
			if ingestFile(root, filepath.Base(root), cfg.SyncGlob, &items, seen) {
				report.Files++
			} else {
				report.Skipped++
			}
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				report.Skipped++
				return nil
			}
			if len(items) >= maxSyncItems || report.Files >= maxSyncFiles {
				truncated = true
				return fs.SkipAll
			}
			if d.IsDir() {
				name := d.Name()
				if path != root && (skipDirs[strings.ToLower(name)] || strings.HasPrefix(name, ".")) {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				report.Skipped++
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = d.Name()
			}
			if ingestFile(path, rel, cfg.SyncGlob, &items, seen) {
				report.Files++
			} else {
				report.Skipped++
			}
			return nil
		})
		if walkErr != nil && walkErr != fs.SkipAll {
			report.Skipped++
		}
	}

	if len(items) == 0 {
		report.Error = "no files matched sync_paths/sync_glob"
		if truncated {
			report.Error = "scan limit reached before a matching file"
		}
		_ = db.SetSkillState(s.ID, store.Now(), report.Error, 0)
		return report
	}
	for i := range items {
		items[i].SkillID = s.ID
	}
	// Replace the derived docs atomically; captured notes are untouched.
	if err := db.ClearSkillItemsByKind(s.ID, "doc"); err != nil {
		report.Error = err.Error()
		_ = db.SetSkillState(s.ID, store.Now(), report.Error, 0)
		return report
	}
	if err := db.CreateSkillItems(items); err != nil {
		report.Error = err.Error()
		_ = db.SetSkillState(s.ID, store.Now(), report.Error, 0)
		return report
	}
	if truncated {
		report.Error = fmt.Sprintf("scan truncated at %d files / %d items", report.Files, len(items))
	}
	report.Items = len(items)
	_ = db.SetSkillState(s.ID, now, report.Error, report.Items)
	return report
}

// cleanSyncPaths trims, cleans and deduplicates configured paths.
func cleanSyncPaths(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// ingestFile reads one text file, applies the glob and size/binary limits, and
// appends one or more chunked items. It returns true when the file was used.
func ingestFile(path, rel, pattern string, items *[]model.SkillItem, seen map[string]bool) bool {
	if !matchGlob(pattern, filepath.Base(rel)) && !matchGlob(pattern, rel) {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if seen[abs] {
		return false
	}
	seen[abs] = true

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil || len(data) == 0 {
		return false
	}
	cut := len(data) > maxFileBytes
	if cut {
		data = data[:maxFileBytes]
	}
	// Reject binary files: NUL bytes in the head mean this is not text.
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	if !utf8.Valid(head) || strings.IndexByte(string(head), 0) >= 0 {
		return false
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return false
	}
	chunks := chunkText(text, chunkRunes, chunkOverlap)
	source := filepath.ToSlash(filepath.Clean(rel))
	if source == "." || source == "" {
		source = filepath.Base(path)
	}
	for i, chunk := range chunks {
		if len(*items) >= maxSyncItems {
			break
		}
		title := source
		if len(chunks) > 1 {
			title = fmt.Sprintf("%s #%d", source, i+1)
		}
		if cut && i == len(chunks)-1 {
			title += " (truncated)"
		}
		*items = append(*items, model.SkillItem{
			ID:        store.NewID("ski"),
			Kind:      "doc",
			Source:    source,
			Title:     title,
			Content:   chunk,
			CreatedAt: store.Now(),
		})
	}
	return true
}

// chunkText splits text into rune windows with overlap so a passage near a
// boundary is still retrievable.
func chunkText(text string, size, overlap int) []string {
	r := []rune(text)
	if len(r) <= size {
		return []string{text}
	}
	if overlap < 0 || overlap >= size {
		overlap = 0
	}
	var out []string
	step := size - overlap
	if step <= 0 {
		step = size
	}
	for start := 0; start < len(r); start += step {
		end := start + size
		if end > len(r) {
			end = len(r)
		}
		out = append(out, string(r[start:end]))
		if end == len(r) {
			break
		}
	}
	return out
}

// matchGlob matches a base name or relative path against a glob that may use
// brace expansion ("*.{go,ts}") and a leading "**\/" passthrough.
func matchGlob(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	for _, p := range expandBraces(pattern) {
		p = strings.TrimPrefix(p, "**/")
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
		if ok, _ := filepath.Match(p, filepath.Base(name)); ok {
			return true
		}
	}
	return false
}

// expandBraces expands "*.{go,ts}" into ["*.go", "*.ts"].
func expandBraces(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	close := strings.IndexByte(pattern[open:], '}')
	if close < 0 {
		return []string{pattern}
	}
	close += open
	prefix := pattern[:open]
	suffix := pattern[close+1:]
	out := []string{}
	for _, alt := range strings.Split(pattern[open+1:close], ",") {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		out = append(out, expandBraces(prefix+alt+suffix)...)
	}
	if len(out) == 0 {
		return []string{pattern}
	}
	return out
}
