package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gitignore "github.com/denormal/go-gitignore"
)

// ----- read -----

// readDefaultLimit matches the Claude Code semantics ROADMAP cites: 2000
// lines is enough to inspect most source files in one shot but small
// enough that the LLM context doesn't fill on a single read of a large
// generated file.
const readDefaultLimit = 2000

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type readTool struct{ tracker *ReadTracker }

func newReadTool(t *ReadTracker) Tool { return &readTool{tracker: t} }

func (t *readTool) Name() string { return "read" }
func (t *readTool) Description() string {
	return "Read a UTF-8 text file. Lines are returned with 1-based line numbers as a TAB-prefixed gutter (e.g. \"42\\thello\"). Defaults to the first 2000 lines; use offset/limit to page."
}
func (t *readTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "Absolute or working-directory-relative file path."},
			"offset": map[string]any{"type": "integer", "description": "1-based starting line. Default 1."},
			"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to return. Default 2000."},
		},
		"required": []string{"path"},
	}
}

func (t *readTool) Call(_ context.Context, raw json.RawMessage) (any, error) {
	var a readArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Path == "" {
		return nil, errors.New("read: missing required 'path' argument. Provide an absolute or working-directory-relative file path to read.")
	}
	if a.Offset <= 0 {
		a.Offset = 1
	}
	if a.Limit <= 0 {
		a.Limit = readDefaultLimit
	}

	f, err := os.Open(a.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("read: file %q does not exist. Verify the path; use the 'glob' tool to discover files (e.g. pattern \"**/*.go\") if you're unsure.", a.Path)
		}
		return nil, fmt.Errorf("read: cannot open %q: %w", a.Path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Lines past 1 MiB are unusual in source code but happen in minified
	// JS / generated SQL — lift the default to avoid spurious errors.
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var (
		lines    []string
		lineNo   = 1
		emitted  = 0
		truncated bool
	)
	for sc.Scan() {
		if lineNo >= a.Offset {
			if emitted >= a.Limit {
				truncated = true
				break
			}
			lines = append(lines, fmt.Sprintf("%d\t%s", lineNo, sc.Text()))
			emitted++
		}
		lineNo++
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	t.tracker.MarkRead(a.Path)
	return map[string]any{
		"path":      a.Path,
		"offset":    a.Offset,
		"limit":     a.Limit,
		"truncated": truncated,
		"content":   strings.Join(lines, "\n"),
	}, nil
}

// ----- write -----

type writeArgs struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
}

type writeTool struct{}

func newWriteTool() Tool { return &writeTool{} }

func (writeTool) Name() string { return "write" }
func (writeTool) Description() string {
	return "Create a file with the given content. Fails if the file already exists unless overwrite=true. Parent directories are created as needed."
}
func (writeTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string"},
			"overwrite": map[string]any{"type": "boolean", "description": "Allow replacing an existing file."},
		},
		"required": []string{"path", "content"},
	}
}

func (writeTool) Call(_ context.Context, raw json.RawMessage) (any, error) {
	var a writeArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Path == "" {
		return nil, errors.New("write: missing required 'path' argument. Provide an absolute or working-directory-relative file path to create.")
	}
	if !a.Overwrite {
		if _, err := os.Stat(a.Path); err == nil {
			return nil, fmt.Errorf("write: %q already exists. The 'write' tool is for creating new files so it refuses to clobber existing content by default. To modify the file in place, use the 'edit' tool (after reading it). To intentionally replace its contents, retry 'write' with overwrite=true.", a.Path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"path": a.Path, "bytes": len(a.Content)}, nil
}

// ----- edit -----

// editArgs covers the three modes the spec lists. We use one struct rather
// than a discriminated union because the field set per mode is small and
// the LLM does better with one tool than three near-identical tools.
type editArgs struct {
	Path         string `json:"path"`
	Mode         string `json:"mode"`          // "replace" | "insert" | "delete"
	Find         string `json:"find"`          // replace + delete: text to locate
	Replace      string `json:"replace"`       // replace: replacement text
	After        int    `json:"after"`         // insert: 1-based line number to insert after (0 = top)
	Text         string `json:"text"`          // insert: content to insert
	All          bool   `json:"all"`           // replace: replace every occurrence (default first only)
	Anchor       string `json:"anchor"`        // optional: text to locate as a proximity anchor; search for 'find' within ±anchor_radius lines
	AnchorRadius int    `json:"anchor_radius"` // default 80; lines to search on each side of the anchor
}

type editTool struct{ tracker *ReadTracker }

func newEditTool(t *ReadTracker) Tool { return &editTool{tracker: t} }

func (editTool) Name() string { return "edit" }
func (editTool) Description() string {
	return "Edit a file in place. Modes: 'replace' (find/replace text, set all=true for every occurrence), 'insert' (add text after a given 1-based line number, 0 means top), 'delete' (remove the given text). The file MUST have been read with the read tool earlier in this session."
}
func (editTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":          map[string]any{"type": "string"},
			"mode":          map[string]any{"type": "string", "enum": []string{"replace", "insert", "delete"}},
			"find":          map[string]any{"type": "string", "description": "replace/delete: text to locate. Must match the file content exactly."},
			"replace":       map[string]any{"type": "string", "description": "replace: replacement text. Inserted verbatim."},
			"after":         map[string]any{"type": "integer", "description": "insert: 1-based line number; new text appears after this line. Use 0 to prepend."},
			"text":          map[string]any{"type": "string", "description": "insert: content to insert. Inserted verbatim."},
			"all":           map[string]any{"type": "boolean", "description": "replace: replace every occurrence; default false (first only)."},
			"anchor":        map[string]any{"type": "string", "description": "optional: nearby text that unambiguously identifies the region. When set, 'find' is only searched within ±anchor_radius lines of the anchor."},
			"anchor_radius": map[string]any{"type": "integer", "description": "lines to search on each side of the anchor. Default 80."},
		},
		"required": []string{"path", "mode"},
	}
}

func (e editTool) Call(_ context.Context, raw json.RawMessage) (any, error) {
	var a editArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Path == "" {
		return nil, errors.New("edit: missing required 'path' argument. Provide the file path you want to modify.")
	}
	if !e.tracker.WasRead(a.Path) {
		return nil, fmt.Errorf("edit: %q was not read in this session, so editing is refused. The 'edit' tool requires you to read a file with the 'read' tool first — this guarantees you have seen the file's current contents and can target an exact, whitespace-sensitive match. Call 'read' on this path, then retry 'edit' with the precise text from the file you want to change.", a.Path)
	}
	body, err := os.ReadFile(a.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("edit: file %q no longer exists. It may have been deleted or moved since you read it. Re-discover its location with 'glob' or 'grep' and read it again before editing.", a.Path)
		}
		return nil, fmt.Errorf("edit: cannot read %q: %w", a.Path, err)
	}
	original := string(body)

	// Normalise CRLF → LF so that find/anchor strings copied from the
	// `read` tool (which outputs LF via bufio.Scanner) match the file
	// content. We restore the original line ending style on write-back.
	usesCRLF := strings.Contains(original, "\r\n")
	if usesCRLF {
		original = strings.ReplaceAll(original, "\r\n", "\n")
	}
	fileLines := strings.Split(original, "\n")

	// Default anchor radius.
	if a.Anchor != "" && a.AnchorRadius <= 0 {
		a.AnchorRadius = 80
	}

	var updated string
	var changed int
	switch a.Mode {
	case "replace":
		if a.Find == "" {
			return nil, errors.New("edit (replace): 'find' is empty. Replace mode locates an exact substring and swaps it for 'replace' — set 'find' to the text you want to change (copy it verbatim from the file you read).")
		}

		// Determine the search region. When an anchor is supplied we
		// locate it via exact substring match, then restrict searches
		// to ±anchor_radius around it — this lets the LLM disambiguate
		// when the same text appears in multiple places.
		searchStart, searchEnd := 0, len(fileLines)
		anchorLine := -1
		if a.Anchor != "" {
			anchorLine = findSubstringLine(original, a.Anchor)
			if anchorLine < 0 {
				return nil, matchFailureError(fileLines, a.Anchor, a.Path, "anchor",
					"Re-read the file with 'read' and copy the anchor text verbatim — it must uniquely identify a region in the file.")
			}
			searchStart = max(0, anchorLine-a.AnchorRadius)
			searchEnd = min(len(fileLines), anchorLine+a.AnchorRadius+1)
		}

		if a.All {
			// All: replace every occurrence in the full file. Anchor
			// is irrelevant because we match everywhere.
			updated = strings.ReplaceAll(original, a.Find, a.Replace)
			changed = strings.Count(original, a.Find)
		} else if a.Anchor != "" {
			// Single hit within the anchor region: find every
			// occurrence and pick the one closest to the anchor line.
			matchPos := findOccurrenceInRegion(original, a.Find, searchStart, searchEnd, anchorLine)
			if matchPos >= 0 {
				updated = original[:matchPos] + a.Replace + original[matchPos+len(a.Find):]
				changed = 1
			}
		} else {
			// Full file, first match.
			updated = strings.Replace(original, a.Find, a.Replace, 1)
			if updated != original {
				changed = 1
			}
		}

		if changed == 0 {
			return nil, matchFailureError(fileLines, a.Find, a.Path, "find",
				"Re-read the file with 'read' and copy the target text verbatim, including a line or two of surrounding context to make it unambiguous.")
		}

	case "insert":
		lines := fileLines
		if a.After < 0 || a.After > len(lines) {
			return nil, fmt.Errorf("edit (insert): 'after'=%d is outside the file's line range [0, %d]. Use 0 to prepend at the top, a 1-based line number to insert immediately after that line, or %d to append at the end.", a.After, len(lines), len(lines))
		}
		head := append([]string{}, lines[:a.After]...)
		tail := append([]string{}, lines[a.After:]...)
		head = append(head, a.Text)
		head = append(head, tail...)
		updated = strings.Join(head, "\n")
		changed = 1

	case "delete":
		if a.Find == "" {
			return nil, errors.New("edit (delete): 'find' is empty. Delete mode removes the first exact match of 'find' from the file — set it to the text you want to remove (copy it verbatim from the file).")
		}

		// Determine the search region (narrowed by anchor if supplied).
		searchStart, searchEnd := 0, len(fileLines)
		anchorLine := -1
		if a.Anchor != "" {
			anchorLine = findSubstringLine(original, a.Anchor)
			if anchorLine < 0 {
				return nil, matchFailureError(fileLines, a.Anchor, a.Path, "anchor",
					"Re-read the file with 'read' and copy the anchor text verbatim — it must uniquely identify a region in the file.")
			}
			searchStart = max(0, anchorLine-a.AnchorRadius)
			searchEnd = min(len(fileLines), anchorLine+a.AnchorRadius+1)
		}

		found := false
		if a.Anchor != "" {
			matchPos := findOccurrenceInRegion(original, a.Find, searchStart, searchEnd, anchorLine)
			if matchPos >= 0 {
				updated = original[:matchPos] + original[matchPos+len(a.Find):]
				found = true
			}
		} else if strings.Contains(original, a.Find) {
			updated = strings.Replace(original, a.Find, "", 1)
			found = true
		}

		if !found {
			return nil, matchFailureError(fileLines, a.Find, a.Path, "find",
				"Re-read the file with 'read' and copy the target text verbatim.")
		}
		changed = 1

	default:
		return nil, fmt.Errorf("edit: unknown mode %q. Supported modes are 'replace' (find/replace text), 'insert' (add text at/after a line number), and 'delete' (remove a piece of text). Set 'mode' to one of those.", a.Mode)
	}

	// Restore the original line ending style before writing back.
	if usesCRLF {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}

	if err := os.WriteFile(a.Path, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{
		"path":         a.Path,
		"mode":         a.Mode,
		"occurrences":  changed,
		"bytes_before": len(original),
		"bytes_after":  len(updated),
	}, nil
}

// ----- edit helpers -----------------------------------------------------------

// findSubstringLine returns the 0-based line number of the first occurrence
// of needle in the original string. Returns -1 if not found.
func findSubstringLine(original, needle string) int {
	pos := strings.Index(original, needle)
	if pos < 0 {
		return -1
	}
	return strings.Count(original[:pos], "\n")
}

// findOccurrenceInRegion returns the byte offset of the occurrence of `find`
// in `original` that is closest to `anchorLine` among those whose starting
// line is within [lineStart, lineEnd). When anchorLine is -1 the first
// match in the region is returned. Returns -1 when find is empty or no
// occurrence falls within the region.
func findOccurrenceInRegion(original, find string, lineStart, lineEnd, anchorLine int) int {
	if find == "" {
		return -1
	}
	bestPos := -1
	bestDist := -1
	idx := 0
	for {
		pos := strings.Index(original[idx:], find)
		if pos < 0 {
			break
		}
		absPos := idx + pos
		lineNo := strings.Count(original[:absPos], "\n")
		if lineNo >= lineStart && lineNo < lineEnd {
			// Signed distance: positive = after anchor, negative = before.
			// When two occurrences are equally distant in absolute terms,
			// prefer the one at or after the anchor.
			absDist := lineNo - anchorLine
			if absDist < 0 {
				absDist = -absDist
			}
			if bestPos < 0 || absDist < bestDist || (absDist == bestDist && lineNo >= anchorLine) {
				bestPos = absPos
				bestDist = absDist
			}
		}
		idx = absPos + 1
	}
	return bestPos
}

// matchFailureError builds an actionable error message when a find/anchor
// search fails. It includes a preview of the searched region so the LLM can
// see what's actually in the file without an extra round-trip.
func matchFailureError(lines []string, find, path, kind string, advice string) error {
	// Build a preview: first 8 lines of the search region.
	previewN := len(lines)
	if previewN > 8 {
		previewN = 8
	}
	var preview strings.Builder
	for _, l := range lines[:previewN] {
		fmt.Fprintf(&preview, "  %s\n", l)
	}
	if len(lines) > previewN {
		preview.WriteString(fmt.Sprintf("  … (%d more lines)\n", len(lines)-previewN))
	}

	return fmt.Errorf(
		"edit (%s): %q did not match any content in %q.\n"+
			"The match must be an exact copy of the text from the file. "+
			"Re-read the file with 'read' and copy the text verbatim, including a line or two of surrounding context to make it unambiguous.\n"+
			"Searched region (first %d lines):\n%s\n%s",
		kind, find, path, previewN, preview.String(), advice,
	)
}

// ----- glob -----


// ----- glob -----

type globArgs struct {
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit"`
}

type globTool struct{}

func newGlobTool() Tool { return &globTool{} }

func (globTool) Name() string { return "glob" }
func (globTool) Description() string {
	return "Find files matching a glob pattern (supports ** for recursive). Respects .gitignore and never returns anything under .git/. Results are sorted by mtime descending so the most-recently-touched files appear first."
}
func (globTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Glob pattern, e.g. \"src/**/*.go\"."},
			"limit":   map[string]any{"type": "integer", "description": "Maximum number of results. Default 200."},
		},
		"required": []string{"pattern"},
	}
}

func (globTool) Call(_ context.Context, raw json.RawMessage) (any, error) {
	var a globArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Pattern == "" {
		return nil, errors.New("glob: missing required 'pattern' argument. Provide a glob like 'src/**/*.go' — '**' recurses into subdirectories, '*' matches a single path segment.")
	}
	if a.Limit <= 0 {
		a.Limit = 200
	}
	// ignorer is rooted at the nearest enclosing git repo so nested
	// .gitignore files higher up still apply. nil when no usable base
	// could be found — the walker still prunes `.git/` in that case so
	// the only thing lost is .gitignore awareness.
	ignorer := loadGitignore()
	matches, err := globRecursive(a.Pattern, ignorer)
	if err != nil {
		return nil, fmt.Errorf("glob: invalid pattern %q: %w. Patterns follow filepath.Match syntax with '**' for recursion (e.g. 'pkg/**/*_test.go').", a.Pattern, err)
	}
	type entry struct {
		Path  string
		MTime int64
	}
	var entries []entry
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil || st.IsDir() {
			continue
		}
		if pathIgnored(ignorer, m, false) {
			// filepath.Glob's no-`**` branch doesn't see the walker, so
			// the gitignore check has to happen here too.
			continue
		}
		entries = append(entries, entry{Path: m, MTime: st.ModTime().UnixNano()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].MTime > entries[j].MTime })
	if len(entries) > a.Limit {
		entries = entries[:a.Limit]
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return map[string]any{
		"pattern": a.Pattern,
		"count":   len(out),
		"paths":   out,
	}, nil
}

// loadGitignore returns a matcher rooted at the nearest enclosing repo —
// found by walking up from cwd until a `.git` entry is hit — so nested
// `.gitignore` files at any level above cwd are honoured. When no `.git`
// is found we anchor at cwd, which is still useful when an owner edits a
// fresh project (the local `.gitignore` is consulted) but doesn't reach
// parent ignores. Returns nil only when cwd itself can't be resolved.
func loadGitignore() gitignore.GitIgnore {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	base := cwd
	for {
		if _, err := os.Stat(filepath.Join(base, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(base)
		if parent == base {
			base = cwd
			break
		}
		base = parent
	}
	ig, err := gitignore.NewRepository(base)
	if err != nil {
		return nil
	}
	return ig
}

// pathIgnored asks the matcher whether p is excluded. Resolving to an
// absolute path lets the repository pick the correct nested .gitignore.
// A nil matcher (no repo found) means "don't filter" — the caller still
// gets `.git/` pruning from the walker.
//
// The base directory itself is never "ignored" — short-circuit before
// the Absolute call because the underlying library panics on a path that
// exactly equals its base (it tries to slice off the trailing separator
// that isn't there).
func pathIgnored(ig gitignore.GitIgnore, p string, isDir bool) bool {
	if ig == nil {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	if abs == ig.Base() {
		return false
	}
	m := ig.Absolute(abs, isDir)
	return m != nil && m.Ignore()
}

// globRecursive supports the `**` segment by walking when the pattern
// contains it. Without `**` we fall through to filepath.Glob, which is
// faster and avoids walking large trees we don't care about. The ignorer
// is consulted during the walk so ignored directories (and `.git/`) are
// pruned rather than walked then filtered — important for repos with
// `node_modules` and similar large ignored trees.
func globRecursive(pattern string, ig gitignore.GitIgnore) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(pattern)
	}
	// Split the pattern at the first `**`. Walk under the prefix dir,
	// match each path against the rest with filepath.Match.
	idx := strings.Index(pattern, "**")
	root := strings.TrimRight(pattern[:idx], string(filepath.Separator))
	if root == "" {
		root = "."
	}
	suffix := strings.TrimLeft(pattern[idx+2:], string(filepath.Separator))
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate per-entry errors so one perm denial doesn't kill the walk
		}
		// Never descend into `.git/`. The check uses basename so we skip
		// nested `.git` dirs too (submodule workdirs, vendored repos).
		// The `path != root` guard preserves intentional `.git/...` roots.
		if d.IsDir() && d.Name() == ".git" && path != root {
			return filepath.SkipDir
		}
		if pathIgnored(ig, path, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if suffix == "" {
			out = append(out, path)
			return nil
		}
		// Match the tail of `path` (relative to root) against `suffix`.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		ok, err := filepath.Match(suffix, rel)
		if err != nil {
			return err
		}
		if ok {
			out = append(out, path)
			return nil
		}
		// Also try matching just the basename so patterns like `**/*.go`
		// (suffix == `*.go`) hit nested files. filepath.Match doesn't
		// span separators, so walking handles the recursion.
		if !strings.Contains(suffix, string(filepath.Separator)) {
			ok, _ := filepath.Match(suffix, filepath.Base(path))
			if ok {
				out = append(out, path)
			}
		}
		return nil
	})
	return out, err
}
