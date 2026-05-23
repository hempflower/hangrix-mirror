package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseReceivePackRefsAgainstRealPush captures the body of a real
// `git push` (the same wire bytes the agent's git client sends when pushing a
// contribution branch) and feeds it to parseReceivePackRefs. The parser must
// recover the pushed ref — if it returns empty, the per-ref ACL is skipped and
// PostReceive's SyncContribution is never called, so the branch lands on the
// server but is never recognised as a contribution.
func TestParseReceivePackRefsAgainstRealPush(t *testing.T) {
	bare := initBareRepo(t)
	work := initWorkRepoWithCommit(t)

	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info/refs"):
			service := r.URL.Query().Get("service")
			w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
			out := gitOutput(t, "", service[len("git-"):], "--stateless-rpc", "--advertise-refs", bare)
			_, _ = w.Write(packetLine("# service=" + service + "\n"))
			_, _ = w.Write([]byte("0000"))
			_, _ = w.Write(out)
		case strings.HasSuffix(r.URL.Path, "/git-receive-pack"):
			body := readMaybeGzip(t, r)
			capturedBody = body
			// Replay to a real receive-pack so the push completes and git is
			// satisfied (and the bare repo actually gains the ref).
			cmd := exec.Command("git", "receive-pack", "--stateless-rpc", bare)
			cmd.Stdin = bytes.NewReader(body)
			w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
			out, err := cmd.Output()
			if err != nil {
				t.Errorf("replay receive-pack: %v", err)
			}
			_, _ = w.Write(out)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Real push of a contribution branch, exactly like the agent does.
	pushOut := gitTry(t, work, "push", srv.URL+"/repo.git", "HEAD:refs/heads/issue-42/server/fix")
	t.Logf("git push output:\n%s", pushOut)

	if len(capturedBody) == 0 {
		t.Fatal("no receive-pack POST body captured — push did not reach the server")
	}

	refs, _ := parseReceivePackRefs(capturedBody)
	t.Logf("parseReceivePackRefs found %d ref(s): %+v", len(refs), refs)
	if len(refs) == 0 {
		t.Fatal("parseReceivePackRefs returned NO refs for a real push — ACL is skipped and SyncContribution never runs")
	}
	var found bool
	for _, u := range refs {
		if u.RefName == "refs/heads/issue-42/server/fix" {
			found = true
			if strings.ContainsAny(u.RefName, "\n\t ") {
				t.Errorf("RefName has stray whitespace: %q", u.RefName)
			}
		}
	}
	if !found {
		t.Fatalf("pushed ref refs/heads/issue-42/server/fix not among parsed refs: %+v", refs)
	}
}

func readMaybeGzip(t *testing.T, r *http.Request) []byte {
	t.Helper()
	var src io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		src = zr
	}
	b, err := io.ReadAll(src)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

func initBareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo.git")
	gitOutput(t, "", "init", "-q", "--bare", "-b", "main", dir)
	return dir
}

func initWorkRepoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitOutput(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, dir, "add", "a.txt")
	gitOutput(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func gitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func gitTry(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git %s returned error (may be expected): %v", strings.Join(args, " "), err)
	}
	return string(out)
}
