package infra

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// TestCheckFastForwardNewBranchPush reproduces issue #25: pushing to a branch
// that doesn't exist yet causes 500.
//
// The flow under test: gitReceivePack → pre-receive observer →
// CheckFastForward(fsPath, "main", rawNewSHA) → IsAncestor("main", rawNewSHA).
// The rawNewSHA is a 40-char hex commit SHA that exists as a loose object
// (unpacked before PreReceive runs) but has no branch/tag ref pointing at it.
func TestCheckFastForwardNewBranchPush(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "host.git")

	g := NewGoGit(&GoGitDeps{})
	if err := g.Init(bare, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := g.SeedInitialCommit(bare, "main", map[string][]byte{
		"README.md": []byte("# test\n"),
	}, "Tester", "tester@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Get the commit SHA from main so we can create a child commit.
	mainSHA, err := g.ResolveCommit(bare, "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	t.Logf("main SHA: %s", mainSHA)

	// Create a second commit that simulates the new branch's content.
	work := filepath.Join(dir, "work")
	runGit(t, "", "clone", bare, work)
	writeOrFail(t, filepath.Join(work, "hello.txt"), []byte("hello\n"))
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "add", ".")
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-m", "second commit")

	// Get the SHA of the new commit.
	cmd := exec.Command("git", "-C", work, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	newSHA := strings.TrimSpace(string(out))
	t.Logf("new commit SHA: %s", newSHA)

	// At this point the commit exists only in the worktree's .git, not in the
	// bare repo.  Simulate what gitReceivePack does: extract the pack objects
	// into the bare repo, THEN run CheckFastForward.
	//
	// We use `git push` to a temp ref to get the objects into the bare repo
	// without actually creating issue/25.
	runGit(t, work, "push", "origin", fmt.Sprintf("%s:refs/tmp/new-commit", newSHA))
	// Remove the temp ref so only the loose objects remain (simulating
	// the state after unpack-objects but before receive-pack updates refs).
	runGit(t, "", "--git-dir="+bare, "update-ref", "-d", "refs/tmp/new-commit")

	// Now the bare repo has the commit object but no ref pointing at it.
	// This is exactly the state when PreReceive runs.

	// 1. Test IsAncestor directly with raw SHA
	isAncestor, err := g.IsAncestor(bare, "main", newSHA)
	if err != nil {
		t.Fatalf("IsAncestor(main, rawSHA): unexpected error: %v", err)
	}
	t.Logf("IsAncestor(main, %s): %v", newSHA[:7], isAncestor)

	// 2. Test CheckFastForward (the actual code path in the observer)
	isFF, mode, err := g.CheckFastForward(bare, "main", newSHA)
	if err != nil {
		t.Fatalf("CheckFastForward(main, rawSHA): unexpected error: %v", err)
	}
	t.Logf("CheckFastForward: ff=%v mode=%s", isFF, mode)

	// 3. Also test the resolveRef path directly with raw SHA to ensure
	// it doesn't produce a non-ErrRefNotFound error.
	repo, err := openRepo(bare)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	hash, err := resolveRef(repo, newSHA)
	if err != nil {
		t.Fatalf("resolveRef(rawSHA): unexpected error: %v", err)
	}
	t.Logf("resolveRef(rawSHA) = %s", hash.String())

	// 4. Verify that lookupRefHash returns ErrRefNotFound for a raw SHA
	// that is also tried as a branch name (the main concern: does
	// repo.Reference(refs/heads/<sha>) return something other than
	// ErrReferenceNotFound?)
	_, err = lookupRefHash(repo, newSHA)
	if err != nil {
		t.Fatalf("lookupRefHash(rawSHA): unexpected error: %v", err)
	}
}

// TestResolveRefRawSHA validates that resolveRef correctly handles a raw
// 40-char hex SHA that exists only as a loose object (no ref pointing at it).
func TestResolveRefRawSHA(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "host.git")

	g := NewGoGit(&GoGitDeps{})
	if err := g.Init(bare, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := g.SeedInitialCommit(bare, "main", map[string][]byte{
		"README.md": []byte("# test\n"),
	}, "Tester", "tester@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Get the main commit SHA, then create a second commit.
	work := filepath.Join(dir, "work")
	runGit(t, "", "clone", bare, work)
	writeOrFail(t, filepath.Join(work, "hello.txt"), []byte("hello\n"))
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "add", ".")
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-m", "second commit")

	cmd := exec.Command("git", "-C", work, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	newSHA := strings.TrimSpace(string(out))

	// Push the object to the bare repo without creating a branch ref.
	runGit(t, work, "push", "origin", fmt.Sprintf("%s:refs/tmp/new-commit", newSHA))
	runGit(t, "", "--git-dir="+bare, "update-ref", "-d", "refs/tmp/new-commit")

	repo, err := openRepo(bare)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}

	// 1. Raw SHA should resolve via ResolveRevision path in lookupRefHash.
	hash, err := resolveRef(repo, newSHA)
	if err != nil {
		t.Fatalf("resolveRef(%s): unexpected error: %v", newSHA[:7], err)
	}
	if hash.String() != newSHA {
		t.Fatalf("resolveRef returned %s, want %s", hash.String(), newSHA)
	}
	t.Logf("resolveRef(rawSHA) = %s ✓", hash.String())

	// 2. Also verify repo.Reference with the SHA as branch name returns
	// ErrReferenceNotFound (not some other error type).
	branchName := plumbing.NewBranchReferenceName(newSHA)
	t.Logf("branch ref name: %s", branchName)
	_, err = repo.Reference(branchName, true)
	if err == nil {
		t.Logf("note: raw SHA resolved as existing branch (unusual but not an error)")
	} else if err == plumbing.ErrReferenceNotFound {
		t.Logf("branch lookup returned ErrReferenceNotFound as expected ✓")
	} else {
		// This is the interesting case — a non-ErrReferenceNotFound error
		// would propagate up through lookupRefHash → resolveRef → IsAncestor
		// → CheckFastForward → PreReceive → gitReceivePack (500).
		t.Fatalf("Reference(refs/heads/<sha>): unexpected error type: %T %v", err, err)
	}
}

func writeOrFail(t *testing.T, p string, body []byte) {
	t.Helper()
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}
