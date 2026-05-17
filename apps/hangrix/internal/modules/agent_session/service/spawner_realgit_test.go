package service

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitinfra "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/infra"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// realGitPathResolver maps a repo's (owner, name) to a tmp-dir bare path.
// The spawner needs a PathResolver to find the on-disk repo; this test
// helper hands back exactly the paths the seedBareRepo helper wrote to.
type realGitPathResolver struct {
	paths map[string]string // key: owner + "/" + name → fs path
}

func (r *realGitPathResolver) ResolvePath(owner, name string) (string, error) {
	if p, ok := r.paths[owner+"/"+name]; ok {
		return p, nil
	}
	return "", repodomain.ErrRepoNotFound
}

// seedBareRepo creates a bare repo whose default branch tip contains
// the given files. Mirrors the helper in repo/handler/git_http_kind_test.go
// but kept local to avoid an awkward import. Returns the bare-repo
// filesystem path.
//
// Skips the test (rather than failing) when `git` isn't on PATH so the
// suite stays green on minimal CI images.
func seedBareRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	bare := filepath.Join(tmp, "bare.git")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in %s: %v\n%s", args, dir, err, out)
		}
	}

	if err := exec.Command("mkdir", "-p", work).Run(); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := exec.Command("git", "init", "--bare", "-q", "-b", "main", bare).Run(); err != nil {
		t.Fatalf("git init bare: %v", err)
	}
	if err := exec.Command("git", "init", "-q", "-b", "main", work).Run(); err != nil {
		t.Fatalf("git init work: %v", err)
	}
	run(work, "git", "config", "user.email", "t@t")
	run(work, "git", "config", "user.name", "T")

	for path, body := range files {
		cmd := exec.Command("sh", "-c",
			"mkdir -p \"$(dirname \""+work+"/"+path+"\")\" && cat > \""+work+"/"+path+"\"")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("write %s: %v\n%s", path, err, out)
		}
	}
	if len(files) == 0 {
		cmd := exec.Command("sh", "-c", "echo placeholder > \""+work+"/.placeholder\"")
		if err := cmd.Run(); err != nil {
			t.Fatalf("placeholder: %v", err)
		}
	}
	run(work, "git", "add", ".")
	run(work, "git", "commit", "-q", "-m", "init")
	run(work, "git", "push", "-q", bare, "main")
	return bare
}

// TestSpawnerEndToEndRealGit pretends Phase 2's whole flow with a real
// `git` binary: a real bare host repo with a real `.hangrix/agents.yml`
// + a real bare agent repo with a real `agent.yml`. The spawner reads
// blobs via `git cat-file -p`, resolves shas via `git rev-parse`, and
// produces a session row.
//
// This is the closest the package can get to the M7a P2 end-to-end
// exit condition without spinning up real Docker — every server-side
// step from "issue opened" to "session row persisted with snapshot
// frozen" runs against real git output.
func TestSpawnerEndToEndRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	hostYAMLBody := `version: 1
container:
  image: ghcr.io/acme/dev:1.2.3
  env:
    NODE_ENV: development
llm:
  model: claude-sonnet-4-6
roles:
  backend:
    agent: acme/coder@main
    triggers: [issue.opened]
    can: [issue_read, issue_comment, bash]
`
	hostBareRepo := seedBareRepo(t, map[string]string{
		".hangrix/agents.yml": hostYAMLBody,
		"README.md":           "# myproject\n",
	})

	agentManifestBody := `version: 1
kind: agent
entry:
  base_prompt: prompts/system.md
declared_tools:
  - issue_read
  - bash
`
	agentBareRepo := seedBareRepo(t, map[string]string{
		"agent.yml":         agentManifestBody,
		"prompts/system.md": "You are the backend coder.",
	})

	// Wire the spawner with real git + real blob reader. PathResolver
	// is a tiny shim that maps the two (owner/name) tuples to the bare
	// repos we just seeded.
	paths := &realGitPathResolver{
		paths: map[string]string{
			"alice/myproject": hostBareRepo,
			"acme/coder":      agentBareRepo,
		},
	}

	repos := newStubRepoStore()
	repos.add(&repodomain.Repo{
		ID:            1,
		OwnerKind:     repodomain.OwnerKindUser,
		OwnerID:       100,
		OwnerName:     "alice",
		Name:          "myproject",
		DefaultBranch: "main",
		Kind:          repodomain.KindStandard,
	})
	repos.add(&repodomain.Repo{
		ID:            10,
		OwnerKind:     repodomain.OwnerKindUser,
		OwnerID:       200,
		OwnerName:     "acme",
		Name:          "coder",
		DefaultBranch: "main",
		Kind:          repodomain.KindAgent,
	})

	resolver := newStubResolver()
	resolver.addUser("alice", 100)
	resolver.addUser("acme", 200)

	runner := newStubRunnerRepo()
	cfg := &config.Config{
		LLM:    config.LLMConfig{EncryptionKey: testEncryptionKey},
		Server: config.ServerConfig{URL: "http://localhost:8080"},
	}

	s := NewSpawner(&SpawnerDeps{
		Repos:    repos,
		Resolver: resolver,
		Storage:  paths,
		Git:      gitinfra.NewGoGit(&gitinfra.GoGitDeps{}), // real git
		Blob:     NewGitBlobReader(),
		Runner:   runner,
		Config:   cfg,
	})

	got, err := s.OnTrigger(context.Background(), domain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueOpened,
		CauseKind:   domain.CauseKindIssueOpened,
		CauseID:     "issue-1",
		RepoID:      1,
		IssueNumber: 1,
		ActorID:     7,
	})
	if err != nil {
		t.Fatalf("OnTrigger err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	sess := runner.sessions[0]
	if sess.RoleKey != "backend" {
		t.Fatalf("role_key = %q", sess.RoleKey)
	}
	if sess.AgentImage != "ghcr.io/acme/dev:1.2.3" {
		t.Fatalf("agent_image = %q", sess.AgentImage)
	}
	// The frozen agent_sha + repo_sha must look like real git shas
	// (40 lowercase hex chars). The exact value depends on `git
	// commit -m "init"` so we can only assert shape.
	if !isLikelySHA(sess.AgentSHA) {
		t.Fatalf("agent_sha = %q (expected 40-hex sha)", sess.AgentSHA)
	}
	if !isLikelySHA(sess.RepoSHA) {
		t.Fatalf("repo_sha = %q (expected 40-hex sha)", sess.RepoSHA)
	}
	if !strings.HasPrefix(sess.AgentRepo, "acme/coder@") {
		t.Fatalf("agent_repo = %q", sess.AgentRepo)
	}
}

func isLikelySHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
