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
			"find":          map[string]any{"type": "string", "description": "replace/delete: text to locate. Match is whitespace-tolerant: indentation (tabs/spaces) and line endings (CRLF/LF) are normalised before comparison."},
			"replace":       map[string]any{"type": "string", "description": "replace: replacement text. Leading whitespace is auto-adapted to match the source file's indentation style."},
			"after":         map[string]any{"type": "integer", "description": "insert: 1-based line number; new text appears after this line. Use 0 to prepend."},
			"text":          map[string]any{"type": "string", "description": "insert: content to insert. Leading whitespace is auto-adapted to match the source file's indentation style."},
			"all":           map[string]any{"type": "boolean", "description": "replace: replace every occurrence; default false (first only)."},
			"anchor":        map[string]any{"type": "string", "description": "optional: nearby text that unambiguously identifies the region. When set, 'find' is only searched within ±anchor_radius lines of the anchor — tolerates minor indentation drift from a recent `read`."},
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
	fileLines := strings.Split(original, "\n")

	// Detect the file's indentation style once, then reuse for matching
	// and for adapting replacement/inserted text.
	indent := detectIndent(original)

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

		// Determine the search region.  When an anchor is supplied we
		// first locate it, then restrict exact-match and normalised-match
		// searches to ±anchor_radius around it — this lets the LLM
		// disambiguate when the same text appears in multiple places.
		searchStart, searchEnd := 0, len(fileLines)
		anchorLine := -1
		if a.Anchor != "" {
			anchorStart, _ := findLinesNormalized(fileLines, 0, len(fileLines), a.Anchor, indent)
			if anchorStart < 0 {
				return nil, matchFailureError(fileLines, a.Anchor, a.Path, "anchor",
					"Re-read the file with 'read' and copy the anchor text verbatim — it must uniquely identify a region in the file.")
			}
			anchorLine = anchorStart
			searchStart = max(0, anchorStart-a.AnchorRadius)
			searchEnd = min(len(fileLines), anchorStart+a.AnchorRadius+1)
		}

		// Exact substring match.
		if a.All {
			// All: replace every occurrence in the full file.  Anchor
			// is irrelevant because we match everywhere.
			updated = strings.ReplaceAll(original, a.Find, a.Replace)
			changed = strings.Count(original, a.Find)
		} else if a.Anchor != "" {
			// Single hit within the anchor region: find every
			// occurrence and pick the one closest to the anchor line.
			matchPos := findOccurrenceInRegion(original, a.Find, searchStart, searchEnd, anchorLine)
			if matchPos >= 0 {
				repl := a.Replace
				if adapted := adaptIndent(a.Replace, a.Find, indent); adapted != a.Replace {
					repl = adapted
				}
				updated = original[:matchPos] + repl + original[matchPos+len(a.Find):]
				changed = 1
			}
		} else {
			// Full file, first match (legacy path — no anchor).
			updated = strings.Replace(original, a.Find, a.Replace, 1)
			if updated != original {
				changed = 1
			}
		}

		// Adapt indentation on the legacy (no-anchor) path.
		if changed > 0 && a.Anchor == "" {
			adapted := adaptIndent(a.Replace, a.Find, indent)
			if adapted != a.Replace {
				if a.All {
					updated = strings.ReplaceAll(original, a.Find, adapted)
					changed = strings.Count(original, a.Find)
				} else {
					updated = strings.Replace(original, a.Find, adapted, 1)
				}
			}
		}

		if changed == 0 {
			// Exact match failed — try whitespace-tolerant line-based
			// matching within the same (possibly anchor-narrowed) region.
			matchStart, matchEnd := findLinesNormalized(fileLines, searchStart, searchEnd, a.Find, indent)
			if matchStart < 0 {
				region := fileLines[searchStart:searchEnd]
				return nil, matchFailureError(region, a.Find, a.Path, "find",
					"Re-read the file with 'read' and copy the target text verbatim, including a line or two of surrounding context to make it unambiguous.")
			}

			originalFind := strings.Join(fileLines[matchStart:matchEnd], "\n")
			adaptedReplace := adaptIndent(a.Replace, originalFind, indent)
			if a.All {
				updated = strings.ReplaceAll(original, originalFind, adaptedReplace)
				changed = strings.Count(original, originalFind)
			} else {
				updated = strings.Replace(original, originalFind, adaptedReplace, 1)
				if updated != original {
					changed = 1
				}
			}
		}
		if changed == 0 {
			return nil, matchFailureError(fileLines, a.Find, a.Path, "find",
				"Re-read the file with 'read' and copy the target text verbatim.")
		}

	case "insert":
		lines := fileLines
		if a.After < 0 || a.After > len(lines) {
			return nil, fmt.Errorf("edit (insert): 'after'=%d is outside the file's line range [0, %d]. Use 0 to prepend at the top, a 1-based line number to insert immediately after that line, or %d to append at the end.", a.After, len(lines), len(lines))
		}
		// Adapt inserted text indentation to match the file's style.
		adaptedText := adaptInsertIndent(a.Text, original, a.After, indent)
		head := append([]string{}, lines[:a.After]...)
		tail := append([]string{}, lines[a.After:]...)
		head = append(head, adaptedText)
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
			anchorStart, _ := findLinesNormalized(fileLines, 0, len(fileLines), a.Anchor, indent)
			if anchorStart < 0 {
				return nil, matchFailureError(fileLines, a.Anchor, a.Path, "anchor",
					"Re-read the file with 'read' and copy the anchor text verbatim — it must uniquely identify a region in the file.")
			}
				anchorLine = anchorStart
			searchStart = max(0, anchorStart-a.AnchorRadius)
			searchEnd = min(len(fileLines), anchorStart+a.AnchorRadius+1)
		}

		// Try exact match within the region (or full file).
		originalFind := a.Find
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
			// Normalised fallback within the same region.
			matchStart, matchEnd := findLinesNormalized(fileLines, searchStart, searchEnd, a.Find, indent)
			if matchStart < 0 {
				region := fileLines[searchStart:searchEnd]
				return nil, matchFailureError(region, a.Find, a.Path, "find",
					"Re-read the file with 'read' and copy the target text verbatim.")
			}
			originalFind = strings.Join(fileLines[matchStart:matchEnd], "\n")
			updated = strings.Replace(original, originalFind, "", 1)
		}
		changed = 1

	default:
		return nil, fmt.Errorf("edit: unknown mode %q. Supported modes are 'replace' (find/replace text), 'insert' (add text at/after a line number), and 'delete' (remove a piece of text). Set 'mode' to one of those.", a.Mode)
	}

	if err := os.WriteFile(a.Path, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{
		"path":            a.Path,
		"mode":            a.Mode,
		"occurrences":     changed,
		"bytes_before":    len(original),
		"bytes_after":     len(updated),
	}, nil
}

// ----- edit helpers -----------------------------------------------------------

// indentStyle captures the dominant indentation convention in a source file.
type indentStyle struct {
	useTabs bool // true = tabs, false = spaces
	width   int  // number of spaces per indent level (meaningful only when !useTabs)
}

// detectIndent scans the first 500 non-empty lines and returns the dominant
// indentation style. Tabs win when at least 30% of indented lines use them;
// otherwise we measure the most common space-indent depth. The thresholds
// are chosen to give a clear signal on typical source files without being
// thrown off by a handful of alignment-only spaces.
func detectIndent(content string) indentStyle {
	lines := strings.Split(content, "\n")
	tabCount := 0
	spaceWidths := map[int]int{} // indent width → count
	indented := 0
	scanned := 0
	for _, line := range lines {
		if scanned >= 500 {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		scanned++
		leading := leadingWhitespace(line)
		if leading == 0 {
			continue
		}
		indented++
		if line[0] == '\t' {
			tabCount++
		} else {
			// Count only lines whose leading space count is a reasonable
			// indent (≤ 16) — deeper indentation is more likely alignment.
			if leading >= 1 && leading <= 16 {
				spaceWidths[leading]++
			}
		}
	}
	if indented == 0 {
		return indentStyle{useTabs: false, width: 4} // safe default
	}
	// Tabs win when they appear on ≥ 30% of indented lines.
	if float64(tabCount)/float64(indented) >= 0.3 {
		return indentStyle{useTabs: true}
	}
	// Pick the most common space width.
	bestW, bestN := 0, 0
	for w, n := range spaceWidths {
		if n > bestN || (n == bestN && w < bestW) {
			bestW, bestN = w, n
		}
	}
	if bestW == 0 {
		bestW = 4
	}
	return indentStyle{useTabs: false, width: bestW}
}

// leadingWhitespace returns the number of leading whitespace characters
// (tabs + spaces) in s.
func leadingWhitespace(s string) int {
	for i, ch := range s {
		if ch != ' ' && ch != '\t' {
			return i
		}
	}
	return len(s)
}

// normalizeLineForMatch collapses the leading whitespace of a line into an
// indent-level marker so matching is tolerant of tab/space and indent-width
// drift. The marker format uses \x00 delimiters that won't collide with
// real source text.
func normalizeLineForMatch(s string, st indentStyle) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, " \t")
	if strings.TrimSpace(s) == "" {
		return s
	}
	vis := countVisualSpaces(s, st)
	unit := st.width
	if unit <= 0 {
		unit = 4
	}
	// Round visual width to the nearest indent level. The half-unit bias
	// means a 2-space indent rounds to level 1 when unit=4 (2+2>=4),
	// which is the cross-indent-width tolerance we want.
	level := (vis + unit/2) / unit
	trimmed := strings.TrimLeft(s, " \t")
	return fmt.Sprintf("\x00%d\x00%s", level, trimmed)
}

// normalizeLinesForMatch applies normalizeLineForMatch to every line.
func normalizeLinesForMatch(lines []string, st indentStyle) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = normalizeLineForMatch(l, st)
	}
	return out
}

// findLinesNormalized searches for needle in haystack[searchStart:searchEnd]
// using whitespace-tolerant, indent-level-based matching. Both sides are
// normalised using their own detected indent styles so a 2-space find can
// match 4-space (or tab-indented) file content. Returns (startLine, endLine)
// or (-1, -1).
func findLinesNormalized(haystack []string, searchStart, searchEnd int, needle string, fileIndent indentStyle) (int, int) {
	needleLines := strings.Split(needle, "\n")
	needleIndent := detectIndent(needle)

	normNeedle := normalizeLinesForMatch(needleLines, needleIndent)
	normHaystack := normalizeLinesForMatch(haystack, fileIndent)

	for i := searchStart; i <= searchEnd-len(normNeedle); i++ {
		match := true
		for j := range normNeedle {
			if normHaystack[i+j] != normNeedle[j] {
				match = false
				break
			}
		}
		if match {
			return i, i + len(normNeedle)
		}
	}
	return -1, -1
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
			"The match is whitespace-tolerant — indentation (tabs/spaces) and trailing whitespace are normalised. "+
			"Still, the text must be a verbatim copy of what you want to locate.\n"+
			"Searched region (first %d lines):\n%s\n%s",
		kind, find, path, previewN, preview.String(), advice,
	)
}

// adaptIndent converts the leading whitespace of each line in `replacement`
// to match the source file's indentation style. It uses the original matched
// text's leading whitespace as a baseline to deduce the relative indent
// level of each replacement line.
// adaptIndent converts the leading whitespace of each line in `replacement`
// to match the source file's indentation style. It uses indent levels
// (rounded visual-width / indent-unit) so that e.g. a 2-space-indented
// replacement correctly maps to a 4-space or tab-indented file.
func adaptIndent(replacement, originalFind string, st indentStyle) string {
	repLines := strings.Split(replacement, "\n")
	origLines := strings.Split(originalFind, "\n")

	// Detect the replacement's own indent style for accurate level computation.
	repIndent := detectIndent(replacement)

	// Find the first non-empty line in the original matched text, and its indent level.
	origBase := ""
	for _, l := range origLines {
		if strings.TrimSpace(l) != "" {
			origBase = l
			break
		}
	}
	if origBase == "" {
		return convertIndentStyle(replacement, st)
	}
	origLevel := indentLevel(origBase, repIndent)

	// Find the first non-empty line in the replacement, and its indent level.
	var firstNonEmpty string
	for _, l := range repLines {
		if strings.TrimSpace(l) != "" {
			firstNonEmpty = l
			break
		}
	}
	if firstNonEmpty == "" {
		return replacement
	}
	repLevel := indentLevel(firstNonEmpty, repIndent)

	// Delta in indent levels: positive means the replacement is deeper.
	delta := repLevel - origLevel
	targetLevel := origLevel + delta
	if targetLevel < 0 {
		targetLevel = 0
	}

	// Rebuild each replacement line with adapted indentation.
	unit := st.width
	if unit <= 0 {
		unit = 4
	}
	var out []string
	for _, l := range repLines {
		if strings.TrimSpace(l) == "" {
			out = append(out, l)
			continue
		}
		lineLevel := indentLevel(l, repIndent)
		newLevel := targetLevel + (lineLevel - repLevel)
		if newLevel < 0 {
			newLevel = 0
		}
		newVisual := newLevel * unit
		newLeading := makeIndent(newVisual, st)
		trimmed := strings.TrimLeft(l, " \t")
		out = append(out, newLeading+trimmed)
	}
	return strings.Join(out, "\n")
}

func adaptInsertIndent(text, original string, after int, st indentStyle) string {
	fileLines := strings.Split(original, "\n")

	// Find the indent level at the insertion point: use the line at `after`
	// if it's non-empty, otherwise scan backwards.
	refIndent := 0
	if after > 0 && after <= len(fileLines) {
		refLine := fileLines[after-1]
		refIndent = countVisualSpaces(refLine, st)
	}
	// Also check the next line for context (e.g. inserting between two
	// indented blocks).
	if after < len(fileLines) {
		nextLine := fileLines[after]
		if strings.TrimSpace(nextLine) != "" {
			nextIndent := countVisualSpaces(nextLine, st)
			if nextIndent < refIndent || refIndent == 0 {
				refIndent = nextIndent
			}
		}
	}

	insertLines := strings.Split(text, "\n")
	// Find the minimum indentation among non-empty lines in the inserted
	// text — this is the "base" indent we'll use as the delta origin.
	minIndent := -1
	for _, l := range insertLines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		vis := countVisualSpaces(l, st)
		if minIndent < 0 || vis < minIndent {
			minIndent = vis
		}
	}
	if minIndent < 0 {
		minIndent = 0
	}

	delta := refIndent - minIndent
	var out []string
	for _, l := range insertLines {
		if strings.TrimSpace(l) == "" {
			out = append(out, l)
			continue
		}
		vis := countVisualSpaces(l, st)
		newIndent := vis + delta
		if newIndent < 0 {
			newIndent = 0
		}
		newLeading := makeIndent(newIndent, st)
		trimmed := strings.TrimLeft(l, " \t")
		out = append(out, newLeading+trimmed)
	}
	return strings.Join(out, "\n")
}

// countVisualSpaces computes the visual width of the leading whitespace
// in s given the indent style. Tabs count as st.width spaces.
func countVisualSpaces(s string, st indentStyle) int {
	w := 0
	tabW := st.width
	if tabW <= 0 {
		tabW = 4
	}
	for _, ch := range s {
		switch ch {
		case ' ':
			w++
		case '\t':
			w += tabW
		default:
			return w
		}
	}
	return w
}

// makeIndent produces a leading-whitespace string of visualWidth spaces,
// using the file's preferred style (tabs or spaces).
func makeIndent(visualWidth int, st indentStyle) string {
	if st.useTabs {
		tabW := st.width
		if tabW <= 0 {
			tabW = 4
		}
		tabs := visualWidth / tabW
		spaces := visualWidth % tabW
		return strings.Repeat("\t", tabs) + strings.Repeat(" ", spaces)
	}
	return strings.Repeat(" ", visualWidth)
}

// indentLevel computes the indent level of a line: visual indent width
// divided by the indent unit, rounded to the nearest integer.
func indentLevel(s string, st indentStyle) int {
	vis := countVisualSpaces(s, st)
	unit := st.width
	if unit <= 0 {
		unit = 4
	}
	return (vis + unit/2) / unit
}



// convertIndentStyle converts all leading whitespace in s to the target
// style without adjusting the relative indent depth. Used as a fallback
// when the original matched text has no non-empty lines.
func convertIndentStyle(s string, st indentStyle) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		vis := countVisualSpaces(l, indentStyle{useTabs: false, width: 4}) // measure assuming 4-space tabs
		// Re-measure with the actual style
		vis = countVisualSpaces(l, st)
		trimmed := strings.TrimLeft(l, " \t")
		lines[i] = makeIndent(vis, st) + trimmed
	}
	return strings.Join(lines, "\n")
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
