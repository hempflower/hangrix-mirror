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
// an issue branch rewrites `.hangrix/agents.yml` to declare new roles
// AND drops the matching `.hangrix/prompts/<key>.md` files alongside.
// After merge, the platform must see both the new yaml and the new
// prompt files at the default-branch tip.
func TestMergeAddsNewAgentsAndPromptFiles(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "host.git")

	g := NewGoGit(&GoGitDeps{})
	if err := g.Init(bare, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Seed mirrors what `repo.InitOnDisk(..., seedReadme=true)` does:
	// one initial commit shipping the bundled template's seeder yaml.
	seederYaml := []byte(`version: 1
container:
  image: ubuntu:22.04
llm:
  model: deepseek-v4-pro
roles:
  seeder:
    triggers:
      issue.opened: {}
    can: [read]
    prompt: |
      seed the repo
`)
	if err := g.SeedInitialCommit(bare, "main", map[string][]byte{
		".hangrix/agents.yml": seederYaml,
	}, "Tester", "tester@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Push the seeder's output via shell git — same as the agent's
	// `bash` tool + `git push` does in production.
	work := filepath.Join(dir, "work")
	runGit(t, "", "clone", bare, work)
	runGit(t, work, "checkout", "-b", "issue/1")
	mustMkdir(t, filepath.Join(work, ".hangrix/prompts"))
	newYaml := []byte(`version: 1
container:
  image: ubuntu:22.04
llm:
  model: deepseek-v4-pro
roles:
  maintainer:
    triggers:
      issue.opened: {}
      issue.comment: {}
    can: [issue_read, issue_comment]
    prompt_file: .hangrix/prompts/maintainer.md
  backend:
    triggers:
      issue.comment:
        mentioned_only: true
    can: [issue_read, issue_comment, read, write, edit]
    prompt_file: .hangrix/prompts/backend.md
`)
	mustWrite(t, filepath.Join(work, ".hangrix/agents.yml"), newYaml)
	mustWrite(t, filepath.Join(work, ".hangrix/prompts/maintainer.md"),
		[]byte("# Maintainer\nYou route work.\n"))
	mustWrite(t, filepath.Join(work, ".hangrix/prompts/backend.md"),
		[]byte("# Backend\nYou write features.\n"))
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "add", ".")
	runGit(t, work, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-m", "seed team")
	runGit(t, work, "push", "origin", "issue/1")

	// Push a divergent commit on main so the merge has to go through
	// the real three-way path (not fast-forward) — that's the path
	// that re-builds the merged tree object via mergeTrees +
	// buildTreeFromFlatMap. Suspecting a tree-flattening bug.
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

	// 1. The merged agents.yml must parse and expose both new roles.
	yamlBody := readBlobOrFail(t, bare, "main", ".hangrix/agents.yml")
	t.Logf("post-merge agents.yml:\n%s", string(yamlBody))
	cfg, err := agentsconfig.ParseHostConfig(yamlBody)
	if err != nil {
		t.Fatalf("ParseHostConfig: %v", err)
	}
	for _, want := range []string{"maintainer", "backend"} {
		if _, ok := cfg.Roles[want]; !ok {
			t.Fatalf("post-merge agents.yml missing role %q; roles=%v", want, mapKeys(cfg.Roles))
		}
	}

	// 2. The prompt files must be reachable at the same ref. The
	//    spawner reads them via the same `git cat-file -p` path.
	for _, p := range []string{".hangrix/prompts/maintainer.md", ".hangrix/prompts/backend.md"} {
		body := readBlobOrFail(t, bare, "main", p)
		if !strings.HasPrefix(string(body), "#") {
			t.Fatalf("blob at %s looks empty/wrong: %q", p, string(body))
		}
	}
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
