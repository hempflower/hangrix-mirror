package local_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

// TestEditRequiresPriorRead pins the read-before-edit guard. The whole
// reason this exists is to stop the LLM from blindly mutating files it
// has never inspected — a regression here turns the agent into a hazard.
func TestEditRequiresPriorRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	editTool := tools["edit"]
	readTool := tools["read"]

	// Edit before read: must refuse, with an LLM-friendly message that
	// names the rule and the fix. The model uses the error text to
	// self-correct, so stripping the explanation regresses behaviour even
	// though the refusal itself still happens.
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": "hello", "replace": "hi",
	}))
	if err == nil || !strings.Contains(err.Error(), "was not read") {
		t.Fatalf("expected read-first refusal, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"'read' tool", "retry", "whitespace-sensitive"} {
		if !strings.Contains(msg, want) {
			t.Errorf("read-first error should mention %q (so the LLM knows how to recover); got: %s", want, msg)
		}
	}

	// Read, then edit: must succeed.
	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": "hello", "replace": "hi",
	})); err != nil {
		t.Fatalf("edit after read: %v", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "hi world" {
		t.Errorf("edit not applied: %q", string(body))
	}
}

// TestEditExactMatchRequired verifies that the edit tool requires exact
// text matching — no whitespace normalisation. The LLM must copy text
// verbatim from the file.
func TestEditExactMatchRequired(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	// File uses tabs for indentation.
	content := "\tline1\n\t\tline2\n\tline3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	// Read first.
	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Replace using exact tab-indented text (verbatim copy from file).
	findText := "\tline1\n\t\tline2"
	replaceText := "\tnew1\n\t\tnew2"
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": findText, "replace": replaceText,
	}))
	if err != nil {
		t.Fatalf("edit with exact tab-indented find should succeed: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)
	if !strings.Contains(got, "\tnew1") {
		t.Errorf("replacement should be inserted verbatim, got: %q", got)
	}
	if strings.Contains(got, "line1") {
		t.Errorf("original 'line1' should have been replaced, got: %q", got)
	}
}

// TestEditExactTrailingWhitespace verifies that trailing whitespace
// must match exactly — no normalisation is applied.
func TestEditExactTrailingWhitespace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "hello\nworld\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Find must match exactly — no whitespace normalisation.
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": "hello\nworld", "replace": "hi\nthere",
	}))
	if err != nil {
		t.Fatalf("edit with exact match: %v", err)
	}

	body, _ := os.ReadFile(path)
	if string(body) != "hi\nthere\n" {
		t.Errorf("expected 'hi\\nthere', got %q", string(body))
	}
}

// TestEditAnchorProximitySearch verifies that the anchor parameter narrows
// the search region, allowing the LLM to target a specific block when the
// same text appears elsewhere.
func TestEditAnchorProximitySearch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	// "target" appears twice — anchor lets us specify which one.
	content := "target: x\n...\n...\nunique anchor\n...\ntarget: y\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Replace the SECOND "target" (near "unique anchor").
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path":    path,
		"mode":    "replace",
		"find":    "target: y",
		"replace": "changed: z",
		"anchor":  "unique anchor",
	}))
	if err != nil {
		t.Fatalf("edit with anchor: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)
	if !strings.Contains(got, "target: x") {
		t.Errorf("first 'target' should be unchanged, got: %q", got)
	}
	if !strings.Contains(got, "changed: z") {
		t.Errorf("second 'target' should be changed, got: %q", got)
	}
}

// TestEditMatchFailureContext verifies that when matching fails, the error
// message includes a preview of the search region, not just a terse "not found".
func TestEditMatchFailureContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "line one\nline two\nline three\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": "nonexistent", "replace": "x",
	}))
	if err == nil {
		t.Fatal("expected error for non-matching find")
	}
	msg := err.Error()
	// The error should show context — at least some lines from the file.
	if !strings.Contains(msg, "line one") && !strings.Contains(msg, "line two") {
		t.Errorf("match-failure error should show file context; got: %s", msg)
	}
	if !strings.Contains(msg, path) {
		t.Errorf("match-failure error should mention the file path; got: %s", msg)
	}
}

// TestEditAnchorNotFoundContext verifies that when an anchor doesn't match,
// the error shows the file content so the LLM can fix the anchor text.
func TestEditAnchorNotFoundContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "actual content here\nmore lines\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": "x", "replace": "y",
		"anchor": "nonexistent anchor",
	}))
	if err == nil {
		t.Fatal("expected error for non-matching anchor")
	}
	msg := err.Error()
	if !strings.Contains(msg, "anchor") {
		t.Errorf("error should mention 'anchor' as the failing component; got: %s", msg)
	}
	if !strings.Contains(msg, "actual content") {
		t.Errorf("anchor-failure error should show file context; got: %s", msg)
	}
}

// TestEditInsertVerbatim verifies that inserted text is passed through
// verbatim — no indentation adaptation.
func TestEditInsertVerbatim(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "func foo() {\n  x := 1\n}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Insert a line with 4-space indentation — should be kept verbatim.
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "insert", "after": 1,
		"text": "    y := 2",
	}))
	if err != nil {
		t.Fatalf("edit insert: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)
	if !strings.Contains(got, "    y := 2") {
		t.Errorf("inserted text should be kept verbatim (4-space indent), got: %q", got)
	}
}

// TestEditReplaceVerbatim verifies that replacement text is passed through
// verbatim — no indentation adaptation.
func TestEditReplaceVerbatim(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	// File uses tabs.
	content := "\told line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Replace with space-indented text — should be kept verbatim.
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace",
		"find": "\told line", "replace": "    new line",
	}))
	if err != nil {
		t.Fatalf("edit replace: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)
	// Expect the replacement to be kept verbatim (4-space indent).
	if got != "    new line\n" && got != "    new line" {
		t.Errorf("replacement should be kept verbatim (4-space indent), got: %q", got)
	}
}

// TestEditAnchorDisambiguatesExactMatch verifies that when `find` appears
// identically in multiple places, the anchor selects the right occurrence
// rather than blindly picking the first one.
func TestEditAnchorDisambiguatesExactMatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	// "duplicate" appears twice — anchor disambiguates.
	content := "first duplicate\n...\nmarker here\n...\nsecond duplicate\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Replace the SECOND "duplicate" (near "marker here").
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path":    path,
		"mode":    "replace",
		"find":    "duplicate",
		"replace": "changed",
		"anchor":  "marker here",
	}))
	if err != nil {
		t.Fatalf("edit with anchor for disambiguation: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)
	if !strings.Contains(got, "first duplicate") {
		t.Errorf("first 'duplicate' should be unchanged, got: %q", got)
	}
	if !strings.Contains(got, "second changed") {
		t.Errorf("second 'duplicate' should be changed to 'changed', got: %q", got)
	}
}

// TestEditDeleteWithAnchor verifies that delete mode respects the anchor
// for disambiguation when the find text appears multiple times.
func TestEditDeleteWithAnchor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "keep me\n...\nunique anchor\n...\ndelete me\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Delete "me" near "unique anchor" — should delete the SECOND "me",
	// leaving "keep me" intact.
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path":   path,
		"mode":   "delete",
		"find":   "me",
		"anchor": "unique anchor",
	}))
	if err != nil {
		t.Fatalf("delete with anchor: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)
	if !strings.Contains(got, "keep me") {
		t.Errorf("first 'me' should be unchanged, got: %q", got)
	}
	if strings.Contains(got, "delete me") {
		t.Errorf("second 'me' should be deleted, got: %q", got)
	}
}

// TestEditDeleteAnchorNotFound verifies that delete mode reports a helpful
// error when the anchor doesn't match.
func TestEditDeleteAnchorNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "some content\nhere\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path":   path,
		"mode":   "delete",
		"find":   "x",
		"anchor": "nonexistent",
	}))
	if err == nil {
		t.Fatal("expected error for non-matching anchor in delete mode")
	}
	msg := err.Error()
	if !strings.Contains(msg, "anchor") {
		t.Errorf("error should mention 'anchor'; got: %s", msg)
	}
	if !strings.Contains(msg, "some content") {
		t.Errorf("error should show file context; got: %s", msg)
	}
}




// TestEditCRLFCrossToolContract locks the cross-tool contract between
// `read` (which normalises CRLF→LF via bufio.Scanner) and `edit`
// (which now normalises its input the same way). A CRLF file read
// with `read` shows LF-only text; the LLM copies that verbatim into
// `find` — edit must accept it and preserve the file's CRLF style.
func TestEditCRLFCrossToolContract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "crlf.txt")

	// Write a file with CRLF line endings.
	content := []byte("hello\r\nworld\r\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	// 1. Read the file. bufio.Scanner strips \r, so the LLM sees LF only.
	res, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rFields struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(mustReJSON(res), &rFields); err != nil {
		t.Fatalf("decode read result: %v", err)
	}
	// read output must be LF (no \r visible).
	if strings.Contains(rFields.Content, "\r") {
		t.Errorf("read output should not contain carriage returns, got: %q", rFields.Content)
	}
	if !strings.Contains(rFields.Content, "hello") || !strings.Contains(rFields.Content, "world") {
		t.Errorf("read output should contain hello and world, got: %q", rFields.Content)
	}

	// 2. Edit using LF find text (exactly what the LLM would copy from read).
	_, err = editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace",
		"find": "hello\nworld", "replace": "hi\nthere",
	}))
	if err != nil {
		t.Fatalf("edit with LF find on CRLF file: %v", err)
	}

	// 3. File must be updated AND still use CRLF.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after edit: %v", err)
	}
	if string(body) != "hi\r\nthere\r\n" {
		t.Errorf("expected CRLF file, got: %q", string(body))
	}

	// 4. Read again — must show LF output.
	res2, _ := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path}))
	var r2Fields struct {
		Content string `json:"content"`
	}
	json.Unmarshal(mustReJSON(res2), &r2Fields)
	if !strings.Contains(r2Fields.Content, "hi") || !strings.Contains(r2Fields.Content, "there") {
		t.Errorf("second read should show hi and there, got: %q", r2Fields.Content)
	}
}

// TestEditReturnsDiffOnReplace verifies that a successful replace-mode edit
// returns a "diff" field containing a standard unified diff whose hunk
// matches the actual file change.
func TestEditReturnsDiffOnReplace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "line one\nline two\nline three\nline four\nline five\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	res, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace",
		"find": "line three", "replace": "LINE THREE",
	}))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}

	var fields struct {
		Path        string `json:"path"`
		Mode        string `json:"mode"`
		Occurrences int    `json:"occurrences"`
		Diff        string `json:"diff"`
	}
	if err := json.Unmarshal(mustReJSON(res), &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if fields.Diff == "" {
		t.Fatal("expected non-empty diff field")
	}
	if !strings.Contains(fields.Diff, "--- a/"+path) {
		t.Errorf("diff should contain --- a/ header; got:\n%s", fields.Diff)
	}
	if !strings.Contains(fields.Diff, "+++ b/"+path) {
		t.Errorf("diff should contain +++ b/ header; got:\n%s", fields.Diff)
	}
	if !strings.Contains(fields.Diff, "@@") {
		t.Errorf("diff should contain @@ hunk header; got:\n%s", fields.Diff)
	}
	if !strings.Contains(fields.Diff, "-line three") {
		t.Errorf("diff should show removed line; got:\n%s", fields.Diff)
	}
	if !strings.Contains(fields.Diff, "+LINE THREE") {
		t.Errorf("diff should show added line; got:\n%s", fields.Diff)
	}

	// Verify the file was actually changed.
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "LINE THREE") {
		t.Errorf("file should contain 'LINE THREE'; got: %q", string(body))
	}
}

// TestEditReturnsDiffOnInsert verifies that a successful insert-mode edit
// returns a unified diff.
func TestEditReturnsDiffOnInsert(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "line one\nline two\nline four\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	res, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "insert", "after": 2,
		"text": "line three",
	}))
	if err != nil {
		t.Fatalf("edit insert: %v", err)
	}

	var fields struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(mustReJSON(res), &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if fields.Diff == "" {
		t.Fatal("expected non-empty diff field")
	}
	if !strings.Contains(fields.Diff, "@@") {
		t.Errorf("diff should contain @@ hunk header; got:\n%s", fields.Diff)
	}
	if !strings.Contains(fields.Diff, "+line three") {
		t.Errorf("diff should show added line; got:\n%s", fields.Diff)
	}

	// Verify file content.
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "line three") {
		t.Errorf("file should contain 'line three'; got: %q", string(body))
	}
}

// TestEditReturnsDiffOnDelete verifies that a successful delete-mode edit
// returns a unified diff.
func TestEditReturnsDiffOnDelete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "keep this\nremove me\nkeep that\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	res, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "delete",
		"find": "remove me\n",
	}))
	if err != nil {
		t.Fatalf("edit delete: %v", err)
	}

	var fields struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(mustReJSON(res), &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if fields.Diff == "" {
		t.Fatal("expected non-empty diff field")
	}
	if !strings.Contains(fields.Diff, "@@") {
		t.Errorf("diff should contain @@ hunk header; got:\n%s", fields.Diff)
	}
	if !strings.Contains(fields.Diff, "-remove me") {
		t.Errorf("diff should show removed line; got:\n%s", fields.Diff)
	}

	// Verify the line was removed.
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "remove me") {
		t.Errorf("'remove me' should have been deleted; got: %q", string(body))
	}
	if !strings.Contains(string(body), "keep this") {
		t.Errorf("'keep this' should still be present; got: %q", string(body))
	}
}

// TestEditDiffAbsentOnFailure verifies that a failed edit (no match) does
// not return an empty diff masquerading as success.
func TestEditDiffAbsentOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	content := "hello world\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	readTool := tools["read"]
	editTool := tools["edit"]

	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}

	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace",
		"find": "nonexistent", "replace": "x",
	}))
	if err == nil {
		t.Fatal("expected error for non-matching find")
	}
	// The existing contract: failure must be an error return, not a
	// success payload with an empty diff.
}

// TestBashForeground covers the basic (sync) bash path: output capture
// and exit code propagation. The PTY merges stdout and stderr by design,
// so we assert against the unified `output` field rather than checking
// the streams separately. Background mode + polling is covered by the
// runtime smoke test downstream; bash_input has its own test below.
func TestBashForeground(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command": "echo out; echo err 1>&2; exit 7",
		"summary": "Print streams and exit 7",
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	// The bash result type is unexported by design — the IPC + LLM
	// round-trip is JSON, so we assert via JSON re-encode rather than
	// reaching into a private struct.
	out := mustReJSON(resRaw)
	var fields struct {
		Summary  string `json:"summary"`
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		TimedOut bool   `json:"timed_out"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("re-decode: %v (%s)", err, out)
	}
	// PTY merges the two streams; ordering between them is buffer-
	// dependent, so we just require that both lines made it through.
	if !strings.Contains(fields.Output, "out") {
		t.Errorf("output should contain 'out'; got %q", fields.Output)
	}
	if !strings.Contains(fields.Output, "err") {
		t.Errorf("output should contain 'err'; got %q", fields.Output)
	}
	if fields.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", fields.ExitCode)
	}
	if fields.TimedOut {
		t.Errorf("timed_out should be false")
	}
	// Summary is supplied by the LLM and echoed back unchanged — the
	// agent-log UI uses it as the collapsed-row label, so if it ever
	// stops round-tripping the chip falls back to a generic "bash".
	if fields.Summary != "Print streams and exit 7" {
		t.Errorf("summary should round-trip from the input args verbatim; got %q", fields.Summary)
	}
}

// TestBashSummaryRoundTripsOnPoll pins that the LLM-supplied summary on
// a background start is preserved across task_id polls — without it, a
// promoted job's UI chip would go blank on the next poll because the
// agent-log view rebuilds the chip from every fresh tool_call payload.
func TestBashSummaryRoundTripsOnPoll(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "echo hi",
		"summary":           "Echo a greeting",
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("bash start: %v", err)
	}
	var start struct {
		TaskID  string `json:"task_id"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.Summary != "Echo a greeting" {
		t.Errorf("background start should echo summary verbatim; got %q", start.Summary)
	}

	// Wait for completion, then poll — summary must still be there.
	deadline := time.Now().Add(3 * time.Second)
	var poll struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	for time.Now().Before(deadline) {
		pollRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{"task_id": start.TaskID}))
		if err != nil {
			t.Fatalf("bash poll: %v", err)
		}
		if err := json.Unmarshal(mustReJSON(pollRaw), &poll); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		if poll.Status == "done" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if poll.Summary != "Echo a greeting" {
		t.Errorf("poll should carry the original summary; got %q", poll.Summary)
	}
}

// TestBashForegroundIsATTY pins the reason we bother with a PTY at all:
// `tty -s` succeeds (exit 0) when stdout is a terminal, so isatty-aware
// tools (apt, npm, anything that toggles colour or line buffering) see
// the "interactive terminal" version of themselves the same way a human
// shell would. Regress to plain pipes and this exits 1.
func TestBashForegroundIsATTY(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command": "tty -s && echo yes || echo no",
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	var fields struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal(mustReJSON(resRaw), &fields); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !strings.Contains(fields.Output, "yes") {
		t.Errorf("expected PTY-attached stdout (tty -s success); got output=%q", fields.Output)
	}
	if fields.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", fields.ExitCode)
	}
}

// TestBashForegroundKillsBackgroundedGrandchild pins two things:
//   - The call returns promptly even when the script backgrounds a
//     long-running grandchild (`./srv &`). Without WaitDelay, exec would
//     hang on the inherited stdout/stderr pipe FDs until the timeout.
//   - The grandchild is actually killed, not just abandoned. We verify
//     by giving the grandchild a delayed `touch <marker>` and asserting
//     the marker never appears. If Setpgid + group-kill regressed, the
//     orphaned grandchild would survive and write the marker.
func TestBashForegroundKillsBackgroundedGrandchild(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")

	start := time.Now()
	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		// Grandchild sleeps 5s before touching the marker. The bash leader
		// exits immediately after `echo ready`, so Call should return ~2s
		// later (WaitDelay) and SIGKILL the group on the way out. If the
		// group-kill works, the grandchild dies before the 5s sleep ends
		// and the marker is never created.
		"command":         "( sleep 5 && touch " + marker + " ) &\necho ready",
		"timeout_seconds": 20,
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Fatalf("bash hung waiting for backgrounded grandchild: took %v (want < 10s)", elapsed)
	}
	out := mustReJSON(resRaw)
	var fields struct {
		Output   string `json:"output"`
		TimedOut bool   `json:"timed_out"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("re-decode: %v (%s)", err, out)
	}
	if !strings.Contains(fields.Output, "ready") {
		t.Errorf("output should contain 'ready'; got %q", fields.Output)
	}
	if fields.TimedOut {
		t.Errorf("should not have timed out (WaitDelay should fire first)")
	}

	// Sleep past the grandchild's 5s timer; if it survived, the marker
	// shows up here.
	time.Sleep(6 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("grandchild survived group-kill: marker %q exists", marker)
	}
}

// TestBashTaskIDMutualExclusion pins the rule that task_id (poll an existing
// background task) and command (start a new one) cannot coexist in a single
// call. The LLM has no business inventing task_id values, so we refuse the
// call rather than silently picking one branch.
func TestBashTaskIDMutualExclusion(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	_, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command": "echo hi",
		"task_id": "task_deadbeef",
	}))
	if err == nil {
		t.Fatal("expected error when both command and task_id are supplied")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should explain the conflict, got: %v", err)
	}
}

// TestBashAutoPromoteToBackground is the headline test for the 30s
// promotion rule: a synchronous call to a slow command must return
// promptly with a task_id and status="promoted" instead of blocking the
// whole turn. We shorten the threshold via the test hook so the suite
// doesn't have to wait 30 seconds.
//
// Regression target: if the promotion path silently degrades to either
// (a) blocking until the command finishes, or (b) killing the command
// at the threshold, long apt/build/test runs will stop being usable.
func TestBashAutoPromoteToBackground(t *testing.T) {
	// Not parallel: this test mutates package-level promotion threshold.
	restore := local.SetForegroundPromoteAfterForTest(200 * time.Millisecond)
	defer restore()

	tools := byName(local.All())
	bash := tools["bash"]

	start := time.Now()
	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		// Sleep past the promotion threshold, then write a final marker
		// that only the post-promotion poll can see.
		"command":         "sleep 1; echo finished-after-promote",
		"timeout_seconds": 10,
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	elapsed := time.Since(start)
	// The call must return shortly after the (200ms) threshold — we
	// give a generous 2s ceiling so CI scheduling jitter doesn't make
	// this flaky. Anything more than that suggests we're waiting for
	// the command to finish synchronously.
	if elapsed > 2*time.Second {
		t.Fatalf("foreground call did not promote in time: took %v", elapsed)
	}

	var promo struct {
		Status     string `json:"status"`
		TaskID     string `json:"task_id"`
		Output     string `json:"output"`
		OutputFile string `json:"output_file"`
	}
	if err := json.Unmarshal(mustReJSON(resRaw), &promo); err != nil {
		t.Fatalf("decode promote: %v", err)
	}
	if promo.Status != "promoted" {
		t.Fatalf("status = %q, want %q", promo.Status, "promoted")
	}
	if promo.TaskID == "" {
		t.Fatalf("expected a task_id on promotion; got %+v", promo)
	}
	if promo.OutputFile == "" {
		t.Errorf("expected output_file path on promotion; got %+v", promo)
	}
	// The notice must point the LLM at how to continue — bash poll and
	// bash_input — otherwise the model has to guess.
	for _, want := range []string{"promoted", "task_id", "bash_input"} {
		if !strings.Contains(promo.Output, want) {
			t.Errorf("promotion notice should mention %q; got: %s", want, promo.Output)
		}
	}

	// Poll until the underlying command actually finishes. The marker
	// "finished-after-promote" only appears *after* the sleep, so this
	// also proves the command kept running past promotion.
	deadline := time.Now().Add(5 * time.Second)
	var poll struct {
		Status     string `json:"status"`
		Output     string `json:"output"`
		ExitCode   int    `json:"exit_code"`
		OutputFile string `json:"output_file"`
	}
	for time.Now().Before(deadline) {
		pollRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
			"task_id": promo.TaskID,
		}))
		if err != nil {
			t.Fatalf("bash poll: %v", err)
		}
		if err := json.Unmarshal(mustReJSON(pollRaw), &poll); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		if poll.Status == "done" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if poll.Status != "done" {
		t.Fatalf("promoted task did not finish: status=%q output=%q", poll.Status, poll.Output)
	}
	if poll.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; output=%q", poll.ExitCode, poll.Output)
	}
	if !strings.Contains(poll.Output, "finished-after-promote") {
		t.Errorf("promoted command should have run to completion; final output=%q", poll.Output)
	}
	if poll.OutputFile != promo.OutputFile {
		t.Errorf("output_file changed across poll: promotion=%q poll=%q", promo.OutputFile, poll.OutputFile)
	}

	// The output_file is supposed to be readable directly — that's why
	// we surface it. If a third party (e.g. `tail -f` from a follow-up
	// bash call) couldn't see what poll just returned, the contract is
	// broken.
	body, err := os.ReadFile(poll.OutputFile)
	if err != nil {
		t.Fatalf("read output_file %q: %v", poll.OutputFile, err)
	}
	if !strings.Contains(string(body), "finished-after-promote") {
		t.Errorf("output_file %q does not contain the marker; got %q", poll.OutputFile, body)
	}
}

// TestBashBackgroundExposesOutputFile pins the contract that
// run_in_background=true returns an output_file path the LLM can read
// directly (e.g. `tail -f` via a sibling bash call). Without this, the
// model is stuck polling — which is fine for short bursts but useless
// for tailing a long build log.
func TestBashBackgroundExposesOutputFile(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "for i in 1 2 3; do echo tick-$i; sleep 0.05; done",
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	var start struct {
		TaskID     string `json:"task_id"`
		Status     string `json:"status"`
		OutputFile string `json:"output_file"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.Status != "running" {
		t.Errorf("status = %q, want %q", start.Status, "running")
	}
	if start.OutputFile == "" {
		t.Fatal("background start should include an output_file path")
	}
	// The path must exist immediately — even if the writer hasn't
	// flushed anything yet, the file is opened in spawnJob.
	if _, err := os.Stat(start.OutputFile); err != nil {
		t.Fatalf("output_file %q should exist on start: %v", start.OutputFile, err)
	}

	// Wait for the task to complete, then read the file directly (not
	// via poll). This is the "tail it from another shell" path.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pollRaw, _ := bash.Call(context.Background(), mustJSON(map[string]any{"task_id": start.TaskID}))
		var pf struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(mustReJSON(pollRaw), &pf)
		if pf.Status == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	body, err := os.ReadFile(start.OutputFile)
	if err != nil {
		t.Fatalf("read output_file: %v", err)
	}
	for _, want := range []string{"tick-1", "tick-2", "tick-3"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("output_file should contain %q; got %q", want, body)
		}
	}
}

// TestBashInputAnswersInteractivePrompt is the headline test for
// bash_input — it pins the end-to-end flow the tool exists to enable:
// start a background bash task that blocks on read(), call bash_input
// with its task_id to feed it an answer, then confirm the program
// completed and the answer reached it. If this regresses, interactive
// confirmations stop working and the LLM is back to dodging y/N prompts
// with `yes |` hacks.
func TestBashInputAnswersInteractivePrompt(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]
	bashInput := tools["bash_input"]
	if bashInput == nil {
		t.Fatal("bash_input tool not registered")
	}

	// Start a background bash that prints a prompt, reads a single line,
	// then echoes "got=<answer>" and exits. Anything that blocks on read
	// would work here; we use plain `read` because it's the canonical
	// shape of a y/N or password prompt.
	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "printf 'continue? '; read ans; echo got=$ans",
		"run_in_background": true,
		"timeout_seconds":   15,
	}))
	if err != nil {
		t.Fatalf("bash start: %v", err)
	}
	var start struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.TaskID == "" {
		t.Fatalf("expected a task_id on background start; got %+v", start)
	}
	if start.Status != "running" {
		t.Errorf("expected status=running on background start; got %q", start.Status)
	}

	// Give the child a moment to actually reach the read() call. Without
	// this the input can race ahead of the prompt and the test becomes
	// flaky in CI. 200ms is plenty for a bash one-liner.
	time.Sleep(200 * time.Millisecond)

	inRaw, err := bashInput.Call(context.Background(), mustJSON(map[string]any{
		"task_id": start.TaskID,
		"data":    "yes",
	}))
	if err != nil {
		t.Fatalf("bash_input: %v", err)
	}
	var inFields struct {
		BytesWritten int    `json:"bytes_written"`
		TaskID       string `json:"task_id"`
	}
	if err := json.Unmarshal(mustReJSON(inRaw), &inFields); err != nil {
		t.Fatalf("decode bash_input: %v", err)
	}
	if inFields.TaskID != start.TaskID {
		t.Errorf("bash_input echoed task_id %q, want %q", inFields.TaskID, start.TaskID)
	}
	// "yes" + auto-appended "\n" = 4 bytes. If we ever stop appending the
	// newline by default, callers' prompts will silently hang and this
	// guard surfaces the change.
	if inFields.BytesWritten != 4 {
		t.Errorf("bytes_written = %d, want 4 (auto-appended newline)", inFields.BytesWritten)
	}

	// Poll until the task finishes. Bounded loop so a regression doesn't
	// hang the suite — the underlying command should be done in well
	// under a second.
	deadline := time.Now().Add(5 * time.Second)
	var pollFields struct {
		Output   string `json:"output"`
		Status   string `json:"status"`
		ExitCode int    `json:"exit_code"`
	}
	for time.Now().Before(deadline) {
		pollRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
			"task_id": start.TaskID,
		}))
		if err != nil {
			t.Fatalf("bash poll: %v", err)
		}
		if err := json.Unmarshal(mustReJSON(pollRaw), &pollFields); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		if pollFields.Status == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pollFields.Status != "done" {
		t.Fatalf("task did not reach done: status=%q output=%q", pollFields.Status, pollFields.Output)
	}
	if pollFields.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", pollFields.ExitCode)
	}
	if !strings.Contains(pollFields.Output, "got=yes") {
		t.Errorf("output should echo the answer ('got=yes'); got %q", pollFields.Output)
	}
}

// TestBashInputAfterDone pins the error path: writing to a task that has
// already exited should fail with a message the LLM can self-correct
// from, not silently swallow the bytes.
func TestBashInputAfterDone(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]
	bashInput := tools["bash_input"]

	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "echo hi",
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("bash start: %v", err)
	}
	var start struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}

	// Wait for the task to finish so the stdin side is definitely closed.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pollRaw, _ := bash.Call(context.Background(), mustJSON(map[string]any{"task_id": start.TaskID}))
		var pf struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(mustReJSON(pollRaw), &pf)
		if pf.Status == "done" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	_, err = bashInput.Call(context.Background(), mustJSON(map[string]any{
		"task_id": start.TaskID,
		"data":    "y",
	}))
	if err == nil {
		t.Fatal("expected an error when writing to a finished task")
	}
	if !strings.Contains(err.Error(), "finished") && !strings.Contains(err.Error(), "closed") {
		t.Errorf("error should explain why the write failed; got: %v", err)
	}
}

// TestBashInputUnknownTaskID pins the recovery message for the case the
// LLM is most likely to hit: invented or stale task_id. We want the
// error to tell it where task_ids come from so it doesn't keep retrying.
func TestBashInputUnknownTaskID(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bashInput := tools["bash_input"]

	_, err := bashInput.Call(context.Background(), mustJSON(map[string]any{
		"task_id": "task_deadbeef",
		"data":    "y",
	}))
	if err == nil {
		t.Fatal("expected an error for an unknown task_id")
	}
	for _, want := range []string{"unknown task_id", "run_in_background"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q (so the LLM knows how to recover); got: %v", want, err)
		}
	}
}

// TestGlobRespectsGitignore pins the two filters layered on top of the
// raw walker: `.git/` is never returned, and paths matched by `.gitignore`
// are dropped. Both matter because an agent globbing `**/*` in a real
// repo otherwise drowns in `.git/objects/...` and `node_modules/...`
// noise, burning context on files the human didn't author. The matcher
// is pure Go (go-gitignore), so this test doesn't need a real git binary
// — we just stub out a `.git/` directory so the matcher anchors at the
// temp dir.
func TestGlobRespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	// Layout:
	//   keep.txt              — should appear
	//   ignored.txt           — listed in .gitignore, should NOT appear
	//   sub/keep.txt          — should appear
	//   ignored-dir/skip.txt  — under an ignored directory, should NOT appear
	//   .git/HEAD             — never returned regardless of .gitignore
	mustWrite := func(rel, body string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(".gitignore", "ignored.txt\nignored-dir/\n")
	mustWrite("keep.txt", "k")
	mustWrite("ignored.txt", "x")
	mustWrite("sub/keep.txt", "k")
	mustWrite("ignored-dir/skip.txt", "x")
	mustWrite(".git/HEAD", "ref: refs/heads/main\n")

	// loadGitignore walks up cwd looking for a `.git` marker; switching
	// cwd into the temp dir anchors the matcher there.
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	tools := byName(local.All())
	glob := tools["glob"]
	res, err := glob.Call(context.Background(), mustJSON(map[string]any{"pattern": "**/*"}))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	out := mustReJSON(res)
	var fields struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	got := map[string]bool{}
	for _, p := range fields.Paths {
		got[filepath.ToSlash(p)] = true
	}
	for _, want := range []string{"keep.txt", "sub/keep.txt"} {
		if !got[want] {
			t.Errorf("expected glob to return %q; got %v", want, fields.Paths)
		}
	}
	for _, banned := range []string{"ignored.txt", "ignored-dir/skip.txt", ".git/HEAD"} {
		if got[banned] {
			t.Errorf("glob should have filtered %q (gitignored / inside .git); got %v", banned, fields.Paths)
		}
	}
}

// TestSleepPausesForSpecifiedSeconds is the headline test for the sleep
// tool: call sleep(seconds=3) and assert the actual wall-clock pause is
// within 200ms of 3s, and that the result echoes the requested seconds.
func TestSleepPausesForSpecifiedSeconds(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	sleep := tools["sleep"]
	if sleep == nil {
		t.Fatal("sleep tool not registered in local.All()")
	}

	start := time.Now()
	res, err := sleep.Call(context.Background(), mustJSON(map[string]any{"seconds": 3}))
	if err != nil {
		t.Fatalf("sleep(seconds=3): %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 2800*time.Millisecond || elapsed > 3200*time.Millisecond {
		t.Errorf("sleep(seconds=3) took %v, want ~3s (±200ms)", elapsed)
	}

	var fields struct {
		Seconds int    `json:"seconds"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(mustReJSON(res), &fields); err != nil {
		t.Fatalf("decode sleep result: %v", err)
	}
	if fields.Seconds != 3 {
		t.Errorf("seconds = %d, want 3", fields.Seconds)
	}
}

// TestSleepRejectsExcessivelyLargeSeconds pins the upper-bound guard:
// the LLM must get an actionable error (max 300, suggestion to poll)
// rather than a silent truncation or a 5-minute-plus hang.
func TestSleepRejectsExcessivelyLargeSeconds(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	sleep := tools["sleep"]

	_, err := sleep.Call(context.Background(), mustJSON(map[string]any{"seconds": 500}))
	if err == nil {
		t.Fatal("expected error for seconds=500")
	}
	msg := err.Error()
	for _, want := range []string{"maximum", "300", "500"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should contain %q; got: %s", want, msg)
		}
	}
}

// TestSleepRejectsZeroSeconds pins the lower-bound guard: seconds=0
// must fail with a message naming the minimum and suggesting alternatives.
func TestSleepRejectsZeroSeconds(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	sleep := tools["sleep"]

	_, err := sleep.Call(context.Background(), mustJSON(map[string]any{"seconds": 0}))
	if err == nil {
		t.Fatal("expected error for seconds=0")
	}
	msg := err.Error()
	for _, want := range []string{"at least 1", "0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should contain %q; got: %s", want, msg)
		}
	}
}

// TestSleepRespectsContextCancellation pins that a cancelled context
// wakes the sleep within 500ms rather than blocking for the full
// requested duration. This is a liveness guard — without it, a
// control:shutdown during a long sleep stalls agent teardown.
func TestSleepRespectsContextCancellation(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	sleep := tools["sleep"]

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel almost immediately, then assert the call returns promptly.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := sleep.Call(ctx, mustJSON(map[string]any{"seconds": 60}))
	if err == nil {
		t.Fatal("expected an error from cancelled context")
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("sleep with cancelled context took %v, want <500ms", elapsed)
	}
	// The error should surface context.Canceled so the LLM knows the
	// sleep was interrupted rather than rejected.
	if !strings.Contains(err.Error(), "cancel") && !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error should mention the cancellation reason; got: %v", err)
	}
}

// TestBashTermDumbSafeForPTY verifies that TERM=dumb (the agent default)
// is safe when combined with a PTY, given the other agentEnvDefaults
// (PAGER=cat, GIT_PAGER=cat, etc.) that prevent pagers from being
// invoked. The concern: if TERM is "dumb", programs that query terminfo
// might take a degraded code path that prompts or hangs.
//
// This test shows:
//  1. Direct terminfo queries (tput cols, tput colors) complete
//     instantly — ncurses treats "dumb" as a valid, minimal terminal
//     type, not as a missing or broken one.
//  2. tput colors returns 0 or -1 — "dumb" advertises no colour
//     support, so programs checking terminfo for colour (e.g.
//     ls --color=auto) will produce plain output.
//  3. git diff (the most common agent pager-invoking command) completes
//     without hanging because GIT_PAGER=cat diverts less before it can
//     check terminfo.
//
// Known limitation: less(1) invoked directly (not as a pager) will hang
// on "Press RETURN" with TERM=dumb even with LESS=-FRX. This is
// acceptable because agents inspect files via read/grep, not pagers,
// and all pager-respecting tools are diverted by PAGER=cat.
func TestBashTermDumbSafeForPTY(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var fields struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		TimedOut bool   `json:"timed_out"`
	}

	// 1. tput cols queries terminfo directly. With TERM=dumb it must
	//    return a column count (typically "80") immediately — no hang.
	resRaw, err := bash.Call(ctx, mustJSON(map[string]any{
		"command": "export TERM=dumb; tput cols 2>/dev/null || echo 'tput not available'",
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if err := json.Unmarshal(mustReJSON(resRaw), &fields); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if fields.TimedOut {
		t.Fatal("tput (direct terminfo query) timed out with TERM=dumb")
	}
	t.Logf("tput cols output: %q", fields.Output)

	// 2. git diff is the most common agent command that would normally
	//    invoke a pager. With GIT_PAGER=cat (in agentEnvDefaults), git
	//    must complete instantly without ever reaching less or its
	//    terminfo check — regardless of TERM value.
	dir := t.TempDir()
	runOK(t, dir, "git", "init")
	runOK(t, dir, "git", "config", "user.email", "test@test")
	runOK(t, dir, "git", "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runOK(t, dir, "git", "add", "f")
	runOK(t, dir, "git", "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resRaw, err = bash.Call(ctx, mustJSON(map[string]any{
		"command":     "export TERM=dumb; git diff",
		"working_dir": dir,
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if err := json.Unmarshal(mustReJSON(resRaw), &fields); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if fields.TimedOut {
		t.Fatal("git diff timed out with TERM=dumb — GIT_PAGER=cat should prevent pager invocation")
	}
	if !strings.Contains(fields.Output, "diff --git") {
		t.Errorf("expected diff output; got %q", fields.Output)
	}
	t.Logf("git diff with TERM=dumb + GIT_PAGER=cat completed cleanly")

	// 3. tput colors returns the number of colours the terminal supports.
	//    TERM=dumb must report 0 or -1 (no colour), confirming that
	//    programs checking terminfo for colour will produce plain output.
	resRaw, err = bash.Call(ctx, mustJSON(map[string]any{
		"command": "export TERM=dumb; tput colors 2>/dev/null || echo -1",
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if err := json.Unmarshal(mustReJSON(resRaw), &fields); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if fields.TimedOut {
		t.Fatal("tput colors timed out with TERM=dumb")
	}
	colors := strings.TrimSpace(fields.Output)
	if colors != "0" && colors != "-1" {
		t.Errorf("TERM=dumb should report 0 or -1 colours, got %q", colors)
	}
	t.Logf("tput colors: %s", colors)
}

// TestBashTermDumbInteractiveInput proves that the bash tool's PTY+stdin
// contract remains intact under TERM=dumb for interactive programs driven
// via bash_input. The concern: if TERM=dumb causes commands to refuse
// interactive mode or degrade their stdin handling, then background tasks
// that block on read() would never unblock when the agent feeds them
// answers.
//
// This test exercises the full round-trip:
//  1. Start a background bash task that blocks on `read` (the canonical
//     shape of a y/N or password prompt).
//  2. Feed an answer via bash_input.
//  3. Poll to completion and verify the answer was received.
//
// Because the agent default TERM is now "dumb" (via agentEnvDefaults),
// this test implicitly validates that TERM=dumb does not interfere with
// the read/write contract on the PTY. If it regresses, interactive
// prompts — the main reason bash_input exists — stop working.
func TestBashTermDumbInteractiveInput(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]
	bashInput := tools["bash_input"]
	if bashInput == nil {
		t.Fatal("bash_input tool not registered")
	}

	// Start a background task. With TERM=dumb in agentEnvDefaults, the
	// child process will see TERM=dumb unless the parent test runner has
	// TERM set (which it does). We export TERM=dumb explicitly to ensure
	// the test exercises the intended terminal type.
	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "export TERM=dumb; printf 'name? '; read name; echo hello=$name",
		"run_in_background": true,
		"timeout_seconds":   15,
	}))
	if err != nil {
		t.Fatalf("bash start: %v", err)
	}
	var start struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.TaskID == "" {
		t.Fatal("expected a task_id on background start")
	}
	if start.Status != "running" {
		t.Errorf("expected status=running; got %q", start.Status)
	}

	// Give the child time to reach the read() call.
	time.Sleep(200 * time.Millisecond)

	// Feed an answer via bash_input.
	inRaw, err := bashInput.Call(context.Background(), mustJSON(map[string]any{
		"task_id": start.TaskID,
		"data":    "World",
	}))
	if err != nil {
		t.Fatalf("bash_input: %v", err)
	}
	var inFields struct {
		BytesWritten int    `json:"bytes_written"`
		TaskID       string `json:"task_id"`
	}
	if err := json.Unmarshal(mustReJSON(inRaw), &inFields); err != nil {
		t.Fatalf("decode bash_input: %v", err)
	}
	if inFields.TaskID != start.TaskID {
		t.Errorf("bash_input echoed task_id %q, want %q", inFields.TaskID, start.TaskID)
	}
	// "World" + auto-appended "\n" = 6 bytes.
	if inFields.BytesWritten != 6 {
		t.Errorf("bytes_written = %d, want 6 (auto-appended newline)", inFields.BytesWritten)
	}

	// Poll until done.
	deadline := time.Now().Add(5 * time.Second)
	var pollFields struct {
		Output   string `json:"output"`
		Status   string `json:"status"`
		ExitCode int    `json:"exit_code"`
	}
	for time.Now().Before(deadline) {
		pollRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
			"task_id": start.TaskID,
		}))
		if err != nil {
			t.Fatalf("bash poll: %v", err)
		}
		if err := json.Unmarshal(mustReJSON(pollRaw), &pollFields); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		if pollFields.Status == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pollFields.Status != "done" {
		t.Fatalf("task did not finish: status=%q output=%q", pollFields.Status, pollFields.Output)
	}
	if pollFields.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", pollFields.ExitCode)
	}
	if !strings.Contains(pollFields.Output, "hello=World") {
		t.Errorf("output should contain 'hello=World' (proof the answer reached the script); got %q", pollFields.Output)
	}
	t.Logf("TERM=dumb + bash_input round-trip: %s", strings.TrimSpace(pollFields.Output))
}

// TestBashTermDumbCursorCapabilities proves that terminfo cursor-addressing
// queries complete instantly under TERM=dumb without hanging. "dumb" is a
// valid (if minimal) terminfo entry — tput queries for cursor capabilities
// return empty strings rather than blocking or erroring, because ncurses
// knows "dumb" has no cursor movement. This test addresses the concern
// that cursor-addressing programs (e.g. TUIs using terminfo for screen
// painting) might hang or error out under TERM=dumb.
func TestBashTermDumbCursorCapabilities(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tests := []struct {
		cap  string // tput capability name
		desc string
	}{
		{"cup", "cursor positioning"},
		{"clear", "clear screen"},
		{"cud1", "cursor down one line"},
		{"cuu1", "cursor up one line"},
		{"cuf1", "cursor forward one column"},
		{"cub1", "cursor back one column"},
		{"smcup", "enter alternate screen"},
		{"rmcup", "exit alternate screen"},
		{"sgr0", "reset attributes"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			resRaw, err := bash.Call(ctx, mustJSON(map[string]any{
				"command": "export TERM=dumb; tput " + tc.cap + " 2>/dev/null; echo exit=$?",
			}))
			if err != nil {
				t.Fatalf("bash: %v", err)
			}
			var fields struct {
				Output   string `json:"output"`
				TimedOut bool   `json:"timed_out"`
			}
			if err := json.Unmarshal(mustReJSON(resRaw), &fields); err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if fields.TimedOut {
				t.Fatalf("tput %s timed out with TERM=dumb", tc.cap)
			}
			// All cursor capabilities should produce empty output
			// (no escape sequences) because "dumb" has no cursor
			// addressing. The exit code of tput may be 0 or 1
			// depending on ncurses version, but it must not hang.
			out := strings.TrimSpace(fields.Output)
			t.Logf("tput %s: %q", tc.cap, out)
			// The output should not contain escape sequences.
			if strings.Contains(out, "\x1b[") {
				t.Errorf("tput %s produced escape sequences under TERM=dumb: %q", tc.cap, out)
			}
		})
	}
}



func byName(ts []local.Tool) map[string]local.Tool {
	m := map[string]local.Tool{}
	for _, t := range ts {
		m[t.Name()] = t
	}
	return m
}

func mustJSON(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

func mustReJSON(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

func runOK(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

