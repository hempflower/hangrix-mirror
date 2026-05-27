package infra

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
)

// TestListRefs_TagCreatedAt verifies that ListRefs populates CreatedAt for
// annotated and lightweight tags, and leaves it zero for branches.
func TestListRefs_TagCreatedAt(t *testing.T) {
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

	// Clone to create a second commit and tags.
	work := filepath.Join(dir, "work")
	runGit(t, "", "clone", bare, work)
	writeOrFail(t, filepath.Join(work, "hello.txt"), []byte("hello\n"))
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "add", ".")
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-m", "second commit")
	runGit(t, work, "push", "origin", "main")

	// Create an annotated tag.
	runGit(t, work, "-c", "user.email=tag@e", "-c", "user.name=Tagger",
		"tag", "-a", "v1.0", "-m", "release v1.0")

	// Create a lightweight tag.
	runGit(t, work, "tag", "light")

	// Push tags to the bare repo.
	runGit(t, work, "push", "origin", "v1.0")
	runGit(t, work, "push", "origin", "light")

	refs, err := g.ListRefs(bare)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}

	// Find the two tags.
	var annotated, lightweight *domain.Ref
	for _, tag := range refs.Tags {
		switch tag.Name {
		case "v1.0":
			annotated = tag
		case "light":
			lightweight = tag
		}
	}
	if annotated == nil {
		t.Fatal("annotated tag 'v1.0' not found in ListRefs")
	}
	if lightweight == nil {
		t.Fatal("lightweight tag 'light' not found in ListRefs")
	}

	// Branches must have zero CreatedAt (omitempty keeps them out of JSON).
	for _, b := range refs.Branches {
		if !b.CreatedAt.IsZero() {
			t.Errorf("branch %q has non-zero CreatedAt: %v", b.Name, b.CreatedAt)
		}
	}

	// Both tags must have non-zero CreatedAt.
	if annotated.CreatedAt.IsZero() {
		t.Error("annotated tag 'v1.0' has zero CreatedAt")
	}
	if lightweight.CreatedAt.IsZero() {
		t.Error("lightweight tag 'light' has zero CreatedAt")
	}

	// Neither should be in the future.
	now := time.Now()
	if annotated.CreatedAt.After(now) {
		t.Error("annotated tag CreatedAt is in the future")
	}
	if lightweight.CreatedAt.After(now) {
		t.Error("lightweight tag CreatedAt is in the future")
	}

	// Both tags should point at the same commit (the second commit).
	if annotated.SHA != lightweight.SHA {
		t.Errorf("annotated SHA %s != lightweight SHA %s", annotated.SHA[:7], lightweight.SHA[:7])
	}

	t.Logf("annotated tag v1.0 CreatedAt: %v", annotated.CreatedAt)
	t.Logf("lightweight tag light CreatedAt: %v", lightweight.CreatedAt)
}

// TestListRefs_TagCreatedAt_AnnotatedNoTagger exercises the fallback path where
// an annotated tag has a zero tagger timestamp. tagCreatedAt should fall back
// to the target commit's committer time. We craft the tag object with epoch=0
// via git hash-object + update-ref to bypass the API's time.Now() backfill.
func TestListRefs_TagCreatedAt_AnnotatedNoTagger(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "host.git")

	g := NewGoGit(&GoGitDeps{})
	if err := g.Init(bare, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := g.SeedInitialCommit(bare, "main", map[string][]byte{
		"README.md": []byte("# test\n"),
	}, "Committer", "c@e"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Get the commit SHA.
	mainSHA, err := g.ResolveCommit(bare, "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}

	// Craft an annotated tag object with zero tagger time. The format is:
	//   object <sha>
	//   type commit
	//   tag <name>
	//   tagger <name> <email> 0 +0000
	//   (blank line)
	//   <message>
	tagBody := "object " + mainSHA + "\n" +
		"type commit\n" +
		"tag v0.0\n" +
		"tagger Nobody <nobody@x> 0 +0000\n" +
		"\n" +
		"zero-tagger tag\n"

	// Write the tag object into the bare repo via git hash-object -w.
	hashOut := runGitWithStdin(t, "", tagBody,
		"--git-dir="+bare, "hash-object", "-t", "tag", "-w", "--stdin")
	tagHash := strings.TrimSpace(hashOut)

	// Point refs/tags/v0.0 at the tag object.
	runGit(t, "", "--git-dir="+bare, "update-ref", "refs/tags/v0.0", tagHash)

	refs, err := g.ListRefs(bare)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}

	var tag *domain.Ref
	for _, t := range refs.Tags {
		if t.Name == "v0.0" {
			tag = t
			break
		}
	}
	if tag == nil {
		t.Fatal("tag 'v0.0' not found")
	}
	// With zero tagger time, should fall back to the commit's committer time.
	if tag.CreatedAt.IsZero() {
		t.Error("tag CreatedAt is zero — fallback to committer time should have worked")
	}
	t.Logf("zero-tagger annotated tag CreatedAt: %v", tag.CreatedAt)
}

// runGitWithStdin runs git with the given stdin string and returns stdout.
func runGitWithStdin(t *testing.T, cwd, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("git %v failed: %v\nstderr: %s", args, err, string(stderr))
	}
	return string(out)
}
