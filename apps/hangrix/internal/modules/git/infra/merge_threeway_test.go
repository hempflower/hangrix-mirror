package infra

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
)

// TestMergeNonOverlappingSameFile is the regression guard for the file-level
// merge bug: two branches editing DIFFERENT lines of the SAME file must merge
// cleanly via git's line-level three-way merge. The old blob-hash compare
// wrongly flagged this as a conflict because both sides touched the file.
func TestMergeNonOverlappingSameFile(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "host.git")

	g := NewGoGit(&GoGitDeps{})
	if err := g.Init(bare, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := g.SeedInitialCommit(bare, "main", map[string][]byte{
		"foo.txt": []byte("line1\nline2\nline3\nline4\nline5\n"),
	}, "Tester", "tester@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// issue/1 edits the first line.
	work := filepath.Join(dir, "work")
	runGit(t, "", "clone", bare, work)
	runGit(t, work, "checkout", "-b", "issue/1")
	mustWrite(t, filepath.Join(work, "foo.txt"),
		[]byte("ISSUE-EDIT\nline2\nline3\nline4\nline5\n"))
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-am", "edit first line")
	runGit(t, work, "push", "origin", "issue/1")

	// main edits the last line — diverges, forcing the three-way path.
	work2 := filepath.Join(dir, "work2")
	runGit(t, "", "clone", bare, work2)
	mustWrite(t, filepath.Join(work2, "foo.txt"),
		[]byte("line1\nline2\nline3\nline4\nMAIN-EDIT\n"))
	runGit(t, work2, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-am", "edit last line")
	runGit(t, work2, "push", "origin", "main")

	// CheckAutoMerge must report it mergeable...
	ok, mode, hint, err := g.CheckAutoMerge(bare, "main", "issue/1")
	if err != nil {
		t.Fatalf("CheckAutoMerge: %v", err)
	}
	if !ok || mode != "merge-commit" {
		t.Fatalf("CheckAutoMerge: ok=%v mode=%q hint=%q; want ok=true mode=merge-commit", ok, mode, hint)
	}

	// ...and MergeBranch must produce a tree carrying BOTH edits.
	if _, _, err := g.MergeBranch(bare, "main", "issue/1", "Merge issue 1",
		domain.Signature{Name: "Tester", Email: "tester@example.com"}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	merged := string(readBlobOrFail(t, bare, "main", "foo.txt"))
	if !strings.Contains(merged, "ISSUE-EDIT") || !strings.Contains(merged, "MAIN-EDIT") {
		t.Fatalf("merged foo.txt missing one side:\n%s", merged)
	}
}

// TestMergeOverlappingSameLineConflicts confirms genuine overlapping edits to
// the same line still surface as domain.ErrMergeConflict (the case where a
// human/agent really does have to rebase and resolve).
func TestMergeOverlappingSameLineConflicts(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "host.git")

	g := NewGoGit(&GoGitDeps{})
	if err := g.Init(bare, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := g.SeedInitialCommit(bare, "main", map[string][]byte{
		"foo.txt": []byte("line1\nline2\nline3\n"),
	}, "Tester", "tester@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	work := filepath.Join(dir, "work")
	runGit(t, "", "clone", bare, work)
	runGit(t, work, "checkout", "-b", "issue/1")
	mustWrite(t, filepath.Join(work, "foo.txt"), []byte("line1\nISSUE\nline3\n"))
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-am", "issue edits line2")
	runGit(t, work, "push", "origin", "issue/1")

	work2 := filepath.Join(dir, "work2")
	runGit(t, "", "clone", bare, work2)
	mustWrite(t, filepath.Join(work2, "foo.txt"), []byte("line1\nMAIN\nline3\n"))
	runGit(t, work2, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-am", "main edits line2")
	runGit(t, work2, "push", "origin", "main")

	ok, mode, _, err := g.CheckAutoMerge(bare, "main", "issue/1")
	if err != nil {
		t.Fatalf("CheckAutoMerge: %v", err)
	}
	if ok || mode != "conflicted" {
		t.Fatalf("CheckAutoMerge: ok=%v mode=%q; want ok=false mode=conflicted", ok, mode)
	}

	if _, _, err := g.MergeBranch(bare, "main", "issue/1", "Merge issue 1",
		domain.Signature{Name: "Tester", Email: "tester@example.com"}); !errors.Is(err, domain.ErrMergeConflict) {
		t.Fatalf("merge: got err=%v; want ErrMergeConflict", err)
	}
}
