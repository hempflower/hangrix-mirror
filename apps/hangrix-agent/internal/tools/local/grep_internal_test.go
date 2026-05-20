package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepFallbackExcludesGitAndGitignore is the regression test for the
// Go fallback path in the grep tool. It constructs a grepTool with rgPath
// set to "" (forcing the fallback) and verifies three behaviours:
//
//  1. .git/ contents (here .git/HEAD) are excluded from grep results.
//  2. Files and directories matched by .gitignore are excluded.
//  3. Non-ignored files still produce hits.
//
// The test mirrors the pattern used by TestGlobRespectsGitignore: it
// creates a temp dir with a .git/ marker (so loadGitignore anchors the
// matcher there) and chdirs into it for the duration of the test.
func TestGrepFallbackExcludesGitAndGitignore(t *testing.T) {
	dir := t.TempDir()

	mustWrite := func(rel, body string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Layout:
	//   .gitignore              — ignores ignored.txt and ignored-dir/
	//   keep.txt                — contains "needle", should appear
	//   ignored.txt             — contains "needle", listed in .gitignore
	//   sub/keep.txt            — contains "needle", should appear
	//   ignored-dir/skip.txt    — contains "needle", under an ignored dir
	//   .git/HEAD               — contains "needle-in-git", must NOT appear
	mustWrite(".gitignore", "ignored.txt\nignored-dir/\n")
	mustWrite("keep.txt", "needle")
	mustWrite("ignored.txt", "needle")
	mustWrite("sub/keep.txt", "needle")
	mustWrite("ignored-dir/skip.txt", "needle")
	mustWrite(".git/HEAD", "needle-in-git")

	// loadGitignore walks up from cwd looking for .git/; chdir anchors it.
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	// Force the Go fallback path by leaving rgPath empty.
	tool := &grepTool{rgPath: ""}

	raw, err := tool.Call(context.Background(), mustRawJSON(map[string]any{
		"pattern": "needle",
		"path":    ".",
	}))
	if err != nil {
		t.Fatalf("grep fallback: %v", err)
	}

	resJSON, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		Pattern string   `json:"pattern"`
		Count   int      `json:"count"`
		Matches []string `json:"matches"`
	}
	if err := json.Unmarshal(resJSON, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, resJSON)
	}

	// Collect matched file paths (strip line:col suffix).
	matchedFiles := map[string]bool{}
	for _, m := range got.Matches {
		// Format is "path:lineno:line"
		idx := strings.LastIndex(m, ":")
		if idx < 0 {
			continue
		}
		idx2 := strings.LastIndex(m[:idx], ":")
		if idx2 < 0 {
			continue
		}
		matchedFiles[filepath.ToSlash(m[:idx2])] = true
	}

	// Files that SHOULD appear.
	for _, want := range []string{"keep.txt", "sub/keep.txt"} {
		if !matchedFiles[want] {
			t.Errorf("grep fallback should match %q; got matches %v", want, got.Matches)
		}
	}

	// Files/paths that MUST NOT appear.
	for _, banned := range []string{"ignored.txt", "ignored-dir/skip.txt", ".git/HEAD"} {
		if matchedFiles[banned] {
			t.Errorf("grep fallback should have excluded %q (gitignored / inside .git); got matches %v", banned, got.Matches)
		}
	}

	// Sanity: count should be exactly 2 (keep.txt + sub/keep.txt).
	if got.Count != 2 {
		t.Errorf("grep fallback count = %d, want 2 (only non-ignored, non-.git files); matches=%v", got.Count, got.Matches)
	}
}

// TestGrepFallbackGlobFilter verifies that the glob parameter is honoured
// in the Go fallback path — only files whose basename matches the glob are
// searched. This is a companion regression test to the gitignore one above.
func TestGrepFallbackGlobFilter(t *testing.T) {
	dir := t.TempDir()

	mustWrite := func(rel, body string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("a.go", "needle")
	mustWrite("b.txt", "needle")
	mustWrite("sub/c.go", "needle")

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	tool := &grepTool{rgPath: ""}

	raw, err := tool.Call(context.Background(), mustRawJSON(map[string]any{
		"pattern": "needle",
		"path":    ".",
		"glob":    "*.go",
	}))
	if err != nil {
		t.Fatalf("grep fallback with glob: %v", err)
	}

	resJSON, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		Count   int      `json:"count"`
		Matches []string `json:"matches"`
	}
	if err := json.Unmarshal(resJSON, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, resJSON)
	}

	matchedFiles := map[string]bool{}
	for _, m := range got.Matches {
		idx := strings.LastIndex(m, ":")
		if idx < 0 {
			continue
		}
		idx2 := strings.LastIndex(m[:idx], ":")
		if idx2 < 0 {
			continue
		}
		matchedFiles[filepath.ToSlash(m[:idx2])] = true
	}

	if !matchedFiles["a.go"] {
		t.Errorf("grep fallback with glob *.go should match a.go; got %v", got.Matches)
	}
	if matchedFiles["b.txt"] {
		t.Errorf("grep fallback with glob *.go should NOT match b.txt; got %v", got.Matches)
	}
	// sub/c.go has basename "c.go" which matches "*.go".
	if !matchedFiles["sub/c.go"] {
		t.Errorf("grep fallback with glob *.go should match sub/c.go; got %v", got.Matches)
	}

	if got.Count != 2 {
		t.Errorf("grep fallback count = %d, want 2; matches=%v", got.Count, got.Matches)
	}
}

// TestGrepFallbackLimit verifies the limit parameter is honoured in the
// Go fallback path.
func TestGrepFallbackLimit(t *testing.T) {
	dir := t.TempDir()

	// Write a file with many matching lines.
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("needle\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	tool := &grepTool{rgPath: ""}

	raw, err := tool.Call(context.Background(), mustRawJSON(map[string]any{
		"pattern": "needle",
		"path":    ".",
		"limit":   5,
	}))
	if err != nil {
		t.Fatalf("grep fallback with limit: %v", err)
	}

	resJSON, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		Count     int  `json:"count"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(resJSON, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, resJSON)
	}

	if got.Count != 5 {
		t.Errorf("grep fallback count = %d, want 5 (limit); got %+v", got.Count, got)
	}
	if !got.Truncated {
		t.Errorf("grep fallback should report truncated=true when limit is hit")
	}
}
