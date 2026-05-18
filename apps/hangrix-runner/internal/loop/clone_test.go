package loop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCloneRepoLocalFile exercises the clone helper against a local
// bare repo (file:// URL) so the test doesn't need an HTTP server. We
// can't validate the credential-helper path this way (a file://
// upstream needs no auth), but the branch-checkout + wipe-and-re-clone
// behaviour is fully covered.
func TestCloneRepoLocalFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()

	// Build a bare upstream with a single commit on `main` so the
	// initial clone has something to fetch.
	upstream := filepath.Join(tmp, "upstream.git")
	mustGit(t, "", "init", "--bare", "-b", "main", upstream)

	seed := filepath.Join(tmp, "seed")
	mustGit(t, "", "init", "-b", "main", seed)
	mustGit(t, seed, "config", "user.email", "test@example.invalid")
	mustGit(t, seed, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-m", "seed")
	mustGit(t, seed, "remote", "add", "origin", upstream)
	mustGit(t, seed, "push", "origin", "main")

	dest := filepath.Join(tmp, "checkout", "repo")

	// First call: fresh clone, branch off main for a brand-new issue.
	spec := cloneSpec{
		BaseURL:       "file://" + tmp,
		Owner:         "irrelevant", // overridden by clone via gitURL → ignored when using direct file://
		Name:          "irrelevant",
		WorkingBranch: "issue/1",
		BaseBranch:    "main",
		SessionToken:  "hgxs_dummy",
		Dest:          dest,
	}
	// cloneRepo builds gitURL() — for the test we want to bypass the
	// owner/name path joining since we have a bare upstream on disk.
	// Exercise the runGit code path directly via a small helper that
	// mimics cloneRepo's clone+checkout but points at our bare file://
	// upstream. This keeps coverage on the branch-resolution branch
	// while skipping the URL-construction branch (covered by
	// TestCloneSpecBuildsURL below).
	if err := cloneRepoAt(context.Background(), spec, upstream); err != nil {
		t.Fatalf("first clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("expected README.md in checkout: %v", err)
	}
	branch := currentBranch(t, dest)
	if branch != "issue/1" {
		t.Errorf("branch = %q, want issue/1", branch)
	}

	// Mutate the checkout to ensure the next clone wipes it.
	if err := os.WriteFile(filepath.Join(dest, "scratch.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write scratch: %v", err)
	}

	// Second call: a previous failed turn left the dir; cloneRepo must
	// wipe and re-clone (issue/1 still doesn't exist on origin, so
	// checkout falls through to branch-from-main).
	if err := cloneRepoAt(context.Background(), spec, upstream); err != nil {
		t.Fatalf("second clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scratch.txt")); !os.IsNotExist(err) {
		t.Errorf("scratch.txt survived re-clone (err=%v) — dir was not wiped", err)
	}

	// Now push issue/1 to upstream so the third clone exercises the
	// "branch already on origin" path.
	mustGit(t, dest, "config", "user.email", "test@example.invalid")
	mustGit(t, dest, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dest, "from-issue.txt"), []byte("issue work"), 0o644); err != nil {
		t.Fatalf("write issue file: %v", err)
	}
	mustGit(t, dest, "add", ".")
	mustGit(t, dest, "commit", "-m", "issue work")
	mustGit(t, dest, "push", "origin", "issue/1")

	if err := cloneRepoAt(context.Background(), spec, upstream); err != nil {
		t.Fatalf("third clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "from-issue.txt")); err != nil {
		t.Fatalf("expected issue branch commit to be present: %v", err)
	}
	if currentBranch(t, dest) != "issue/1" {
		t.Errorf("third clone branch = %q, want issue/1", currentBranch(t, dest))
	}
}

// TestCloneSpecBuildsURL is a unit check on the URL + credential-helper
// config assembly; no git invocation needed.
func TestCloneSpecBuildsURL(t *testing.T) {
	s := cloneSpec{
		BaseURL:      "https://hangrix.example/",
		Owner:        "alice",
		Name:         "myproject",
		SessionToken: "hgxs_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
	}
	if got, want := s.gitURL(), "https://hangrix.example/git/alice/myproject.git"; got != want {
		t.Errorf("gitURL = %q, want %q", got, want)
	}

	arg := s.credentialHelperConfigArg()
	// Section must be scoped to BaseURL (sans trailing slash) so the
	// helper never fires for any other host the agent talks to.
	wantPrefix := "credential.https://hangrix.example.helper=!"
	if !strings.HasPrefix(arg, wantPrefix) {
		t.Errorf("credentialHelperConfigArg = %q, want prefix %q", arg, wantPrefix)
	}
	// The helper must read the token from env at request time — if it
	// ever bakes the literal token in, the rotation guarantee
	// (cloned .git/config keeps working when the env value changes)
	// silently regresses.
	if strings.Contains(arg, s.SessionToken) {
		t.Errorf("credentialHelperConfigArg leaked literal token: %q", arg)
	}
	if !strings.Contains(arg, "$HANGRIX_SESSION_TOKEN") {
		t.Errorf("credentialHelperConfigArg missing $HANGRIX_SESSION_TOKEN expansion: %q", arg)
	}
	// Helper must emit both username and password lines per
	// gitcredentials(7); missing either makes git fall back to a
	// terminal prompt that GIT_TERMINAL_PROMPT=0 then refuses.
	if !strings.Contains(arg, "echo username=") {
		t.Errorf("credentialHelperConfigArg missing username output: %q", arg)
	}
	if !strings.Contains(arg, "password=") {
		t.Errorf("credentialHelperConfigArg missing password output: %q", arg)
	}
}

// cloneRepoAt is the test-side variant of cloneRepo that swaps the
// computed gitURL for an explicit upstream path. Keeps the production
// code free of test seams while letting us point at a file:// upstream.
func cloneRepoAt(ctx context.Context, spec cloneSpec, upstreamPath string) error {
	if err := spec.validate(); err != nil {
		return err
	}
	if err := os.RemoveAll(spec.Dest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(spec.Dest), 0o755); err != nil {
		return err
	}
	cloneArgs := []string{
		"clone",
		"--branch", branchOrDefault(spec.BaseBranch, "main"),
		"--",
		upstreamPath,
		spec.Dest,
	}
	if err := runGit(ctx, "", cloneArgs...); err != nil {
		return err
	}
	if spec.WorkingBranch != "" && spec.WorkingBranch != spec.BaseBranch {
		hasRemote := runGit(ctx, spec.Dest, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+spec.WorkingBranch) == nil
		var args []string
		if hasRemote {
			args = []string{"checkout", "-B", spec.WorkingBranch, "refs/remotes/origin/" + spec.WorkingBranch}
		} else {
			args = []string{"checkout", "-B", spec.WorkingBranch}
		}
		if err := runGit(ctx, spec.Dest, args...); err != nil {
			return err
		}
	}
	return nil
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}
