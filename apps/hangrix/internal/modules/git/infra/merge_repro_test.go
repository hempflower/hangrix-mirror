package infra

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
)

// TestMergeAddsNewAgentsAndPromptFiles reproduces the seeder-team flow:
// an issue branch rewrites `.hangrix/agents.yml` (team config + tool
// rules) AND drops one `.hangrix/agents/<role>.md` file per new role
// (YAML front matter + Markdown prompt body). After merge, the platform
// must be able to LoadHostConfig at the default-branch tip and see both
// new roles, with their prompts coming from the per-role files.
func TestMergeAddsNewAgentsAndPromptFiles(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "host.git")

	g := NewGoGit(&GoGitDeps{})
	if err := g.Init(bare, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Seed mirrors what `repo.InitOnDisk(..., seedReadme=true)` does:
	// one initial commit shipping the bundled template's seeder team
	// config. We seed the team yaml only (no `.hangrix/agents/` subdir)
	// because SeedInitialCommit's tree encoder sorts entries by plain
	// name, which cannot represent a tree that holds both the file
	// `agents.yml` and the directory `agents/` side by side (git sorts
	// dirs as if they had a trailing '/', so `agents.yml` < `agents/`).
	// The new role files arrive via the issue-branch merge below, whose
	// `git merge-tree` path builds correctly-sorted trees natively.
	seederYaml := []byte(`version: 1
container:
  image: ubuntu:22.04
llm:
  model: deepseek-v4-pro
tools:
  all: ["*"]
`)
	if err := g.SeedInitialCommit(bare, "main", map[string][]byte{
		".hangrix/agents.yml": seederYaml,
	}, "Tester", "tester@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Push the seeder's output via shell git — same as the agent's
	// `bash` tool + `git push` does in production. The issue branch
	// rewrites the team yaml and drops new per-role agent files.
	work := filepath.Join(dir, "work")
	runGit(t, "", "clone", bare, work)
	runGit(t, work, "checkout", "-b", "issue/1")
	mustMkdir(t, filepath.Join(work, ".hangrix/agents"))
	newYaml := []byte(`version: 1
container:
  image: ubuntu:22.04
llm:
  model: deepseek-v4-pro
tools:
  reader: [issue_read, issue_comment]
  writer: [issue_read, issue_comment]
`)
	mustWrite(t, filepath.Join(work, ".hangrix/agents.yml"), newYaml)
	mustWrite(t, filepath.Join(work, ".hangrix/agents/maintainer.md"),
		[]byte(`---
triggers:
  issue.opened: {}
  issue.comment: {}
permission: read
tools: [reader]
---
# Maintainer
You route work.
`))
	mustWrite(t, filepath.Join(work, ".hangrix/agents/backend.md"),
		[]byte(`---
triggers:
  issue.comment:
    mentioned_only: true
permission: write
tools: [writer]
---
# Backend
You write features.
`))
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "add", ".")
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-m", "seed team")
	runGit(t, work, "push", "origin", "issue/1")

	// Push a divergent commit on main so the merge has to go through
	// the real three-way path (not fast-forward) — the `git merge-tree`
	// path that produces the merged tree object.
	work2 := filepath.Join(dir, "work2")
	runGit(t, "", "clone", bare, work2)
	mustWrite(t, filepath.Join(work2, "README.md"), []byte("hello\n"))
	runGit(t, work2, "-c", "user.email=t@e", "-c", "user.name=t", "add", ".")
	runGit(t, work2, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-m", "diverge main")
	runGit(t, work2, "push", "origin", "main")

	mergeSHA, mode, err := g.MergeBranch(bare, "main", "issue/1", "Merge issue 1",
		domain.Signature{Name: "Tester", Email: "tester@example.com"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	t.Logf("merge: sha=%s mode=%s", mergeSHA, mode)

	// 1. LoadHostConfig at the merged tip must assemble both new roles
	//    from the team yaml + per-role agent files. The FileProvider
	//    below reads them the same way the spawner does (git cat-file /
	//    git ls-tree on the bare repo at the default-branch tip).
	fp := &gitFileProvider{t: t, bare: bare, ref: "main"}
	cfg, err := agentsconfig.LoadHostConfig(fp)
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}
	if cfg == nil {
		t.Fatalf("LoadHostConfig returned nil at merged tip")
	}
	for _, want := range []string{"maintainer", "backend"} {
		if _, ok := cfg.Roles[want]; !ok {
			t.Fatalf("post-merge config missing role %q; roles=%v", want, mapKeys(cfg.Roles))
		}
	}

	// 2. The per-role prompt bodies must survive the merge: each role's
	//    prompt is the Markdown body of its `.hangrix/agents/<role>.md`.
	if got := cfg.Roles["maintainer"].Prompt; !strings.HasPrefix(got, "# Maintainer") {
		t.Fatalf("maintainer prompt = %q, want it to start with the file body", got)
	}
	if got := cfg.Roles["backend"].Prompt; !strings.HasPrefix(got, "# Backend") {
		t.Fatalf("backend prompt = %q, want it to start with the file body", got)
	}
}

// gitFileProvider satisfies agentsconfig.FileProvider over a bare repo at
// a fixed ref, mirroring the production GitBlobReader: ReadFile is
// `git cat-file -p <ref>:<path>` and ListDir is
// `git ls-tree --name-only <ref> <dir>/`.
type gitFileProvider struct {
	t    *testing.T
	bare string
	ref  string
}

func (p *gitFileProvider) ReadFile(path string) ([]byte, bool) {
	cmd := exec.CommandContext(context.Background(), "git",
		"--git-dir="+p.bare, "cat-file", "-p", p.ref+":"+path)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

func (p *gitFileProvider) ListDir(dir string) ([]string, bool) {
	cmd := exec.CommandContext(context.Background(), "git",
		"--git-dir="+p.bare, "ls-tree", "--name-only", p.ref,
		strings.TrimSuffix(dir, "/")+"/")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return nil, false
	}
	return paths, true
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, p string, body []byte) {
	t.Helper()
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func readBlobOrFail(t *testing.T, bare, ref, path string) []byte {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git",
		"--git-dir="+bare, "cat-file", "-p", ref+":"+path)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cat-file %s:%s: %v", ref, path, err)
	}
	return out
}

func mapKeys(m map[string]*agentsconfig.Role) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestMergeTreeSortHyphenVsDir reproduces the "encode tree: entries in
// tree are not sorted" bug caused by sorting tree entries by name alone
// instead of using git's directory-with-trailing-slash convention.
//
// When a merge produces a tree that has both a directory named "X" and a
// file named "X-something" at the same level, the plain string sort
// places "X" (dir) before "X-something" (file), but git expects
// "X-something" (file) before "X/" (dir) because '-' (0x2D) < '/' (0x2F).
func TestMergeTreeSortHyphenVsDir(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "host.git")

	g := NewGoGit(&GoGitDeps{})
	if err := g.Init(bare, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Seed with a single file so the repo is not empty.
	if err := g.SeedInitialCommit(bare, "main", map[string][]byte{
		"README.md": []byte("hello\n"),
	}, "Tester", "tester@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Issue branch adds a file named "server-reviewer.md".
	work := filepath.Join(dir, "work")
	runGit(t, "", "clone", bare, work)
	runGit(t, work, "checkout", "-b", "issue/1")
	mustWrite(t, filepath.Join(work, "server-reviewer.md"), []byte("# Reviewer\n"))
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "add", ".")
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-m", "add server-reviewer")
	runGit(t, work, "push", "origin", "issue/1")

	// Divergent commit on main adds a file inside a directory named
	// "server/" — when merged, the root tree will have both the file
	// "server-reviewer.md" and the directory "server/" side by side.
	work2 := filepath.Join(dir, "work2")
	runGit(t, "", "clone", bare, work2)
	mustMkdir(t, filepath.Join(work2, "server"))
	mustWrite(t, filepath.Join(work2, "server", "config.yml"), []byte("port: 8080\n"))
	runGit(t, work2, "-c", "user.email=t@e", "-c", "user.name=t", "add", ".")
	runGit(t, work2, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-m", "add server dir")
	runGit(t, work2, "push", "origin", "main")

	// Three-way merge — exercises the `git merge-tree` path, which builds
	// correctly-sorted trees natively.
	mergeSHA, mode, err := g.MergeBranch(bare, "main", "issue/1", "Merge issue 1",
		domain.Signature{Name: "Tester", Email: "tester@example.com"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	t.Logf("merge: sha=%s mode=%s", mergeSHA, mode)

	// Both files must be reachable.
	rev := readBlobOrFail(t, bare, "main", "server-reviewer.md")
	if string(rev) != "# Reviewer\n" {
		t.Fatalf("server-reviewer.md content: %q", string(rev))
	}
	cfg := readBlobOrFail(t, bare, "main", "server/config.yml")
	if string(cfg) != "port: 8080\n" {
		t.Fatalf("server/config.yml content: %q", string(cfg))
	}
}
