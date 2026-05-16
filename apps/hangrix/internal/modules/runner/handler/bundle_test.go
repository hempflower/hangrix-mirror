package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// ---- test doubles ----

type stubResolver struct {
	owners map[string]*orgdomain.Owner
}

func (s *stubResolver) ResolveOwner(_ context.Context, name string) (*orgdomain.Owner, error) {
	if o, ok := s.owners[name]; ok {
		return o, nil
	}
	return nil, orgdomain.ErrOrgNotFound
}

func (s *stubResolver) Membership(context.Context, int64, int64) (orgdomain.Role, bool, error) {
	return "", false, nil
}

type stubRepoStore struct {
	// keyed by "<ownerKind>:<ownerID>:<repoName>"
	repos map[string]*repodomain.Repo
}

func (s *stubRepoStore) key(k repodomain.OwnerKind, id int64, name string) string {
	return string(k) + ":" + name
}

func (s *stubRepoStore) GetByOwnerAndName(_ context.Context, k repodomain.OwnerKind, id int64, name string) (*repodomain.Repo, error) {
	if r, ok := s.repos[s.key(k, id, name)]; ok {
		return r, nil
	}
	return nil, repodomain.ErrRepoNotFound
}

// Unused Store methods — implemented to satisfy the interface; tests
// fail loud if they're accidentally exercised.
func (s *stubRepoStore) Create(context.Context, repodomain.OwnerKind, int64, string, string, string, repodomain.Visibility) (*repodomain.Repo, error) {
	panic("Create not stubbed")
}
func (s *stubRepoStore) GetByID(context.Context, int64) (*repodomain.Repo, error) {
	panic("GetByID not stubbed")
}
func (s *stubRepoStore) ListByOwner(context.Context, repodomain.OwnerKind, int64, bool, *repodomain.Kind, int32, int32) ([]*repodomain.Repo, int64, error) {
	panic("ListByOwner not stubbed")
}
func (s *stubRepoStore) Delete(context.Context, int64) error { panic("Delete not stubbed") }
func (s *stubRepoStore) UpdateMeta(context.Context, int64, string, string, repodomain.Visibility) (*repodomain.Repo, error) {
	panic("UpdateMeta not stubbed")
}
func (s *stubRepoStore) UpdateKind(context.Context, int64, repodomain.Kind) error {
	panic("UpdateKind not stubbed")
}
func (s *stubRepoStore) Transfer(context.Context, int64, repodomain.OwnerKind, int64) (*repodomain.Repo, error) {
	panic("Transfer not stubbed")
}

type stubPathResolver struct{ paths map[string]string }

func (s *stubPathResolver) ResolvePath(owner, name string) (string, error) {
	if p, ok := s.paths[owner+"/"+name]; ok {
		return p, nil
	}
	return "", repodomain.ErrRepoNotFound
}

// ---- fixture ----

// seedAgentRepo creates a bare git repo in a temp dir with one commit
// containing `agent.yml`. Returns the bare-repo path and the commit sha.
// Skips the test if `git` isn't on PATH.
func seedAgentRepo(t *testing.T) (bareRepo, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	workDir := tmp + "/work"
	bareRepo = tmp + "/bare.git"
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	runInit := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("mkdir", "-p", workDir).Run(); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runInit("git", "init", "-q", "-b", "main", workDir)
	runInit("git", "init", "--bare", "-q", "-b", "main", bareRepo)
	run("git", "config", "user.email", "t@t")
	run("git", "config", "user.name", "T")
	manifest := "version: 1\nkind: agent\nentry:\n  base_prompt: prompts/system.md\n"
	if err := writeFile(workDir+"/agent.yml", manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeFile(workDir+"/prompts/system.md", "You are helpful.\n"); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	run("git", "add", ".")
	run("git", "commit", "-q", "-m", "init")
	run("git", "push", "-q", bareRepo, "main")

	out, err := exec.Command("git", "--git-dir="+bareRepo, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	sha = strings.TrimSpace(string(out))
	return bareRepo, sha
}

func writeFile(path, body string) error {
	cmd := exec.Command("sh", "-c", "mkdir -p \"$(dirname \""+path+"\")\" && cat > \""+path+"\"")
	cmd.Stdin = strings.NewReader(body)
	return cmd.Run()
}

// ---- harness ----

// callBundle invokes h.getAgentBundle through a chi router so URL
// parameters land in chi.URLParam as they would in production.
func callBundle(h *AgentHandler, owner, name, target string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/api/runner/agent-bundles/{owner}/{name}/*", h.getAgentBundle)
	req := httptest.NewRequest(http.MethodGet, "/api/runner/agent-bundles/"+owner+"/"+name+"/"+target, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func newHandlerWithRepo(t *testing.T, kind repodomain.Kind) (*AgentHandler, string, string) {
	t.Helper()
	bare, sha := seedAgentRepo(t)
	owner := "alice"
	repoName := "reviewer"
	h := &AgentHandler{
		orgResolver: &stubResolver{owners: map[string]*orgdomain.Owner{
			owner: {Kind: orgdomain.OwnerKindUser, ID: 42, Name: owner},
		}},
		repos: &stubRepoStore{repos: map[string]*repodomain.Repo{
			"user:" + repoName: {
				ID: 7, OwnerName: owner, Name: repoName,
				Kind:          kind,
				DefaultBranch: "main",
			},
		}},
		paths: &stubPathResolver{paths: map[string]string{
			owner + "/" + repoName: bare,
		}},
	}
	return h, sha, owner
}

// ---- tests ----

func TestGetAgentBundleSuccess(t *testing.T) {
	h, sha, owner := newHandlerWithRepo(t, repodomain.KindAgent)

	w := callBundle(h, owner, "reviewer", sha+".tar.gz")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/gzip" {
		t.Errorf("Content-Type = %q, want application/gzip", got)
	}
	headerSum := w.Header().Get("X-Hangrix-SHA256")
	if headerSum == "" {
		t.Fatal("missing X-Hangrix-SHA256")
	}
	sum := sha256.Sum256(w.Body.Bytes())
	if got := hex.EncodeToString(sum[:]); got != headerSum {
		t.Errorf("header sha %q != body sha %q", headerSum, got)
	}

	// Verify the tarball actually contains agent.yml at the root (no prefix
	// wrapping per the spec) and matching content.
	gz, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, _ := io.ReadAll(tr)
		files[hdr.Name] = string(body)
	}
	if !strings.Contains(files["agent.yml"], "kind: agent") {
		t.Errorf("agent.yml missing or malformed: %q", files["agent.yml"])
	}
	if _, ok := files["prompts/system.md"]; !ok {
		t.Errorf("prompts/system.md missing from tarball")
	}
}

func TestGetAgentBundleIsDeterministic(t *testing.T) {
	h, sha, owner := newHandlerWithRepo(t, repodomain.KindAgent)

	w1 := callBundle(h, owner, "reviewer", sha+".tar.gz")
	// Sleep enough that any "now"-based gzip ModTime would differ.
	time.Sleep(20 * time.Millisecond)
	w2 := callBundle(h, owner, "reviewer", sha+".tar.gz")

	if w1.Body.String() != w2.Body.String() {
		t.Fatalf("bundle bytes drifted between calls (lengths %d vs %d)", w1.Body.Len(), w2.Body.Len())
	}
	if w1.Header().Get("X-Hangrix-SHA256") != w2.Header().Get("X-Hangrix-SHA256") {
		t.Errorf("sha header drifted: %q vs %q", w1.Header().Get("X-Hangrix-SHA256"), w2.Header().Get("X-Hangrix-SHA256"))
	}
}

func TestGetAgentBundle404OnNonAgentRepo(t *testing.T) {
	h, sha, owner := newHandlerWithRepo(t, repodomain.KindStandard)
	w := callBundle(h, owner, "reviewer", sha+".tar.gz")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for non-agent repo", w.Code)
	}
}

func TestGetAgentBundle404OnUnknownOwner(t *testing.T) {
	h, sha, _ := newHandlerWithRepo(t, repodomain.KindAgent)
	w := callBundle(h, "nobody", "reviewer", sha+".tar.gz")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown owner", w.Code)
	}
}

func TestGetAgentBundle404OnUnknownRepo(t *testing.T) {
	h, sha, owner := newHandlerWithRepo(t, repodomain.KindAgent)
	w := callBundle(h, owner, "ghost", sha+".tar.gz")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown repo", w.Code)
	}
}

func TestGetAgentBundle404OnUnreachableSha(t *testing.T) {
	h, _, owner := newHandlerWithRepo(t, repodomain.KindAgent)
	w := callBundle(h, owner, "reviewer", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef.tar.gz")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unreachable sha", w.Code)
	}
}

func TestGetAgentBundle400OnBadExtension(t *testing.T) {
	h, sha, owner := newHandlerWithRepo(t, repodomain.KindAgent)
	w := callBundle(h, owner, "reviewer", sha+".zip")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for non-tar.gz extension", w.Code)
	}
}

func TestGetAgentBundle400OnInvalidSha(t *testing.T) {
	h, _, owner := newHandlerWithRepo(t, repodomain.KindAgent)
	w := callBundle(h, owner, "reviewer", "not-a-sha.tar.gz")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for invalid sha shape", w.Code)
	}
}

// Compile-time guard: stubs implement the interfaces.
var (
	_ orgdomain.Resolver     = (*stubResolver)(nil)
	_ repodomain.Store       = (*stubRepoStore)(nil)
	_ repodomain.PathResolver = (*stubPathResolver)(nil)
	_ domain.SessionStatus   = domain.SessionStatusPending // touch domain to keep import
)
