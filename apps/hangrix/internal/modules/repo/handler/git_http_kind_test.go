package handler

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// stubKindStore is the minimum slice of domain.Store needed by
// refreshRepoKind: only UpdateKind is exercised. Other methods panic so a
// regression that calls into them is loud.
type stubKindStore struct {
	updates []kindCall
	err     error
}

type kindCall struct {
	id   int64
	kind domain.Kind
}

func (s *stubKindStore) UpdateKind(_ context.Context, id int64, k domain.Kind) error {
	s.updates = append(s.updates, kindCall{id: id, kind: k})
	return s.err
}

func (s *stubKindStore) Create(context.Context, domain.OwnerKind, int64, string, string, string, domain.Visibility) (*domain.Repo, error) {
	panic("Create not stubbed")
}
func (s *stubKindStore) GetByID(context.Context, int64) (*domain.Repo, error) {
	panic("GetByID not stubbed")
}
func (s *stubKindStore) GetByOwnerAndName(context.Context, domain.OwnerKind, int64, string) (*domain.Repo, error) {
	panic("GetByOwnerAndName not stubbed")
}
func (s *stubKindStore) ListByOwner(context.Context, domain.OwnerKind, int64, bool, *domain.Kind, int32, int32) ([]*domain.Repo, int64, error) {
	panic("ListByOwner not stubbed")
}
func (s *stubKindStore) Delete(context.Context, int64) error { panic("Delete not stubbed") }
func (s *stubKindStore) UpdateMeta(context.Context, int64, string, string, domain.Visibility) (*domain.Repo, error) {
	panic("UpdateMeta not stubbed")
}
func (s *stubKindStore) Transfer(context.Context, int64, domain.OwnerKind, int64) (*domain.Repo, error) {
	panic("Transfer not stubbed")
}

// seedRepoWith builds a bare git repo whose default branch tip contains
// the given files (path → content). Returns the bare-repo filesystem path.
// Skips the test when `git` isn't on PATH.
func seedRepoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	work := tmp + "/work"
	bare := tmp + "/bare.git"

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
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
		cmd := exec.Command("sh", "-c", "mkdir -p \"$(dirname \""+work+"/"+path+"\")\" && cat > \""+work+"/"+path+"\"")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("write %s: %v\n%s", path, err, out)
		}
	}
	if len(files) == 0 {
		// Empty repo case: still need *something* on the branch to push.
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

// callRefresh constructs a minimal Handler and invokes refreshRepoKind
// against the seeded bare repo, then returns the recorded UpdateKind
// calls so the test can assert which kind was written.
func callRefresh(t *testing.T, fsPath string) []kindCall {
	t.Helper()
	store := &stubKindStore{}
	h := &Handler{store: store}
	repo := &domain.Repo{ID: 99, DefaultBranch: "main"}
	h.refreshRepoKind(context.Background(), repo, fsPath)
	return store.updates
}

func TestRefreshRepoKindStandardWhenNoAgentYml(t *testing.T) {
	bare := seedRepoWith(t, map[string]string{"README.md": "# hello\n"})
	calls := callRefresh(t, bare)
	if len(calls) != 1 {
		t.Fatalf("UpdateKind called %d times, want 1", len(calls))
	}
	if calls[0].kind != domain.KindStandard {
		t.Errorf("kind = %q, want %q", calls[0].kind, domain.KindStandard)
	}
	if calls[0].id != 99 {
		t.Errorf("id = %d, want 99", calls[0].id)
	}
}

func TestRefreshRepoKindAgentWhenValidManifest(t *testing.T) {
	bare := seedRepoWith(t, map[string]string{
		"agent.yml":         "version: 1\nkind: agent\nentry:\n  base_prompt: prompts/system.md\n",
		"prompts/system.md": "be helpful\n",
	})
	calls := callRefresh(t, bare)
	if len(calls) != 1 || calls[0].kind != domain.KindAgent {
		t.Fatalf("calls = %+v, want [{id:99, kind:agent}]", calls)
	}
}

func TestRefreshRepoKindStandardWhenManifestForbiddenField(t *testing.T) {
	// Per principle 7, an agent.yml that declares container / env / secrets
	// must be rejected by the parser. The kind cache should stay standard
	// so the bad file doesn't smuggle the repo into the agent-dispatch
	// pool until the owner fixes it.
	bare := seedRepoWith(t, map[string]string{
		"agent.yml": "version: 1\nkind: agent\nentry:\n  base_prompt: p.md\ncontainer:\n  image: bad\n",
		"p.md":      "x",
	})
	calls := callRefresh(t, bare)
	if len(calls) != 1 || calls[0].kind != domain.KindStandard {
		t.Fatalf("malformed agent.yml should keep kind=standard, got %+v", calls)
	}
}

func TestRefreshRepoKindStandardWhenManifestUnparseable(t *testing.T) {
	bare := seedRepoWith(t, map[string]string{
		"agent.yml": ": : : not yaml at all",
	})
	calls := callRefresh(t, bare)
	if len(calls) != 1 || calls[0].kind != domain.KindStandard {
		t.Fatalf("unparseable agent.yml should keep kind=standard, got %+v", calls)
	}
}

func TestRefreshRepoKindNoOpWhenDefaultBranchEmpty(t *testing.T) {
	// repo.DefaultBranch == "" → handler bails before touching the store.
	store := &stubKindStore{}
	h := &Handler{store: store}
	h.refreshRepoKind(context.Background(), &domain.Repo{ID: 99, DefaultBranch: ""}, "/nonexistent")
	if len(store.updates) != 0 {
		t.Fatalf("UpdateKind unexpectedly called: %+v", store.updates)
	}
}

// Sanity: stub satisfies the interface.
var _ domain.Store = (*stubKindStore)(nil)

// Sanity: an error returned by UpdateKind is swallowed (the doc string
// says "errors are swallowed"), so refreshRepoKind doesn't panic.
func TestRefreshRepoKindSwallowsStoreError(t *testing.T) {
	bare := seedRepoWith(t, map[string]string{"README.md": "# hi"})
	store := &stubKindStore{err: errors.New("boom")}
	h := &Handler{store: store}
	h.refreshRepoKind(context.Background(), &domain.Repo{ID: 1, DefaultBranch: "main"}, bare)
	if len(store.updates) != 1 {
		t.Fatalf("expected 1 update call, got %d", len(store.updates))
	}
}
