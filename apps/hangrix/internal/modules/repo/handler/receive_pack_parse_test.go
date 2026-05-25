package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
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

func TestParseReceivePackRefsAgainstRealTagPush(t *testing.T) {
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

	gitOutput(t, work, "tag", "-f", "v1.0.0")
	pushOut := gitTry(t, work, "push", srv.URL+"/repo.git", "refs/tags/v1.0.0")
	t.Logf("git push output:\n%s", pushOut)

	if len(capturedBody) == 0 {
		t.Fatal("no receive-pack POST body captured — push did not reach the server")
	}

	refs, _ := parseReceivePackRefs(capturedBody)
	if len(refs) == 0 {
		t.Fatal("parseReceivePackRefs returned NO refs for a real tag push")
	}
	var found bool
	for _, u := range refs {
		if u.RefName == "refs/tags/v1.0.0" {
			found = true
			if strings.ContainsAny(u.RefName, "\n\t ") {
				t.Errorf("RefName has stray whitespace: %q", u.RefName)
			}
		}
	}
	if !found {
		t.Fatalf("pushed ref refs/tags/v1.0.0 not among parsed refs: %+v", refs)
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

// ---- injectContributionHints tests ----

func TestInjectContributionHints_Normal(t *testing.T) {
	// Simulate real receive-pack --stateless-rpc output: an outer sideband
	// pkt-line (channel 1) containing inner pkt-line-framed status, followed
	// by an outer "0000" flush.
	var in bytes.Buffer
	inner := fmt.Sprintf("%04x%s", 4+len("unpack ok\n"), "unpack ok\n")
	inner += fmt.Sprintf("%04x%s", 4+len("ok refs/heads/main\n"), "ok refs/heads/main\n")
	inner += "0000" // inner flush
	payload := "\x01" + inner
	fmt.Fprintf(&in, "%04x%s", 4+len(payload), payload)
	in.WriteString("0000") // outer flush

	h := &Handler{}
	contribs := []domain.PostReceiveContrib{
		{ContributionID: 42, RefName: "refs/heads/issue-1/server/fix", AgentRole: "server", HeadSHA: "abc1234def"},
	}
	h.injectContributionHints(&in, contribs)

	out := in.Bytes()

	// Must end with "0000".
	if !bytes.HasSuffix(out, []byte("0000")) {
		t.Fatalf("output does not end with 0000:\n%x", out)
	}

	// Parse the outer stream: there should be the original outer pkt-line,
	// then the contribution progress pkt-lines, then a final "0000".
	pos := 0
	outerFrames := 0
	var progressLines []string
	for pos < len(out) {
		if pos+4 > len(out) {
			t.Fatalf("truncated at pos %d", pos)
		}
		lenHex := string(out[pos : pos+4])
		if lenHex == "0000" {
			pos += 4
			break
		}
		var pl int
		if _, err := fmt.Sscanf(lenHex, "%04x", &pl); err != nil || pl < 5 {
			t.Fatalf("invalid pkt-line at pos %d: len=%q", pos, lenHex)
		}
		ch := out[pos+4]
		payload := out[pos+5 : pos+pl]
		outerFrames++
		switch ch {
		case 1:
			// Original pack-data frame — verify inner structure intact.
			if !bytes.Contains(payload, []byte("unpack ok")) {
				t.Errorf("channel-1 payload missing unpack status")
			}
			if !bytes.HasSuffix(payload, []byte("0000")) {
				t.Errorf("channel-1 payload missing inner flush")
			}
		case 2:
			progressLines = append(progressLines, string(payload))
		default:
			t.Errorf("unexpected channel %d", ch)
		}
		pos += pl
	}
	if pos != len(out) {
		t.Errorf("trailing bytes after flush: %d", len(out)-pos)
	}
	if len(progressLines) != 2 {
		t.Fatalf("expected 2 progress pkt-lines, got %d: %v", len(progressLines), progressLines)
	}
	if !strings.Contains(progressLines[0], "contribution_id: 42") {
		t.Errorf("first progress line missing contribution_id: %q", progressLines[0])
	}
}

func TestInjectContributionHints_NoFlush(t *testing.T) {
	// When receive-pack output lacks a terminating flush pkt (e.g. truncated),
	// the function must still produce a valid stream ending with "0000".
	var in bytes.Buffer
	in.WriteString("0011\x01partial") // incomplete, no flush

	h := &Handler{}
	contribs := []domain.PostReceiveContrib{
		{ContributionID: 1, RefName: "refs/heads/issue-1/server/fix", AgentRole: "server", HeadSHA: "abc1234"},
	}
	h.injectContributionHints(&in, contribs)

	out := in.Bytes()
	if !bytes.HasSuffix(out, []byte("0000")) {
		t.Fatalf("output must end with 0000 even when input lacks one:\n%x", out)
	}
}

func TestInjectContributionHints_Empty(t *testing.T) {
	var in bytes.Buffer

	h := &Handler{}
	contribs := []domain.PostReceiveContrib{
		{ContributionID: 1, RefName: "refs/heads/issue-1/server/fix", AgentRole: "server", HeadSHA: "abc1234"},
	}
	h.injectContributionHints(&in, contribs)

	out := in.Bytes()
	if !bytes.HasSuffix(out, []byte("0000")) {
		t.Fatalf("empty input must still produce valid stream ending with 0000:\n%x", out)
	}
	// Should contain the hints + flush.
	if !bytes.Contains(out, []byte("contribution_id: 1")) {
		t.Errorf("empty input output missing contributions")
	}
}

func TestInjectContributionHints_OnlyFlush(t *testing.T) {
	// Input is just "0000" (empty push).
	var in bytes.Buffer
	in.WriteString("0000")

	h := &Handler{}
	contribs := []domain.PostReceiveContrib{
		{ContributionID: 7, RefName: "refs/heads/issue-7/server/thing", AgentRole: "server", HeadSHA: "def5678"},
	}
	h.injectContributionHints(&in, contribs)

	out := in.Bytes()
	if !bytes.HasSuffix(out, []byte("0000")) {
		t.Fatalf("output must end with 0000:\n%x", out)
	}
	if !bytes.Contains(out, []byte("contribution_id: 7")) {
		t.Errorf("contributions missing when input was only flush")
	}
}

func TestInjectContributionHints_NoOuterFlush(t *testing.T) {
	// Simulate receive-pack killed after writing the sideband-1 pkt-line
	// (whose payload includes the inner "0000" flush) but BEFORE writing
	// the outer "0000" flush. This is the exact scenario described in
	// issue #157: bytes.LastIndex found the inner "0000", splitting
	// inside the outer pkt-line and corrupting the framing.
	var in bytes.Buffer
	inner := fmt.Sprintf("%04x%s", 4+len("unpack ok\n"), "unpack ok\n")
	inner += fmt.Sprintf("%04x%s", 4+len("ok refs/heads/main\n"), "ok refs/heads/main\n")
	inner += "0000" // inner flush
	payload := "\x01" + inner
	fmt.Fprintf(&in, "%04x%s", 4+len(payload), payload)
	// Deliberately NO outer "0000" — the stream is truncated.

	h := &Handler{}
	contribs := []domain.PostReceiveContrib{
		{ContributionID: 42, RefName: "refs/heads/issue-1/server/fix", AgentRole: "server", HeadSHA: "abc1234def"},
	}
	h.injectContributionHints(&in, contribs)

	out := in.Bytes()

	// Must end with "0000" (appended by the injector).
	if !bytes.HasSuffix(out, []byte("0000")) {
		t.Fatalf("output does not end with 0000:\n%x", out)
	}

	// Walk the output as pkt-lines. The original sideband-1 frame must be
	// intact — its length prefix must match the data length exactly.
	pos := 0
	foundPack := false
	foundProgress := false
	for pos < len(out) {
		if pos+4 > len(out) {
			t.Fatalf("truncated at pos %d", pos)
		}
		lenHex := string(out[pos : pos+4])
		if lenHex == "0000" {
			pos += 4
			break
		}
		var pl int
		if _, err := fmt.Sscanf(lenHex, "%04x", &pl); err != nil || pl < 5 {
			t.Fatalf("invalid pkt-line at pos %d: len=%q", pos, lenHex)
		}
		if pos+pl > len(out) {
			t.Fatalf("pkt-line at pos %d claims %d bytes but only %d remain", pos, pl, len(out)-pos)
		}
		ch := out[pos+4]
		payload := out[pos+5 : pos+pl]
		switch ch {
		case 1:
			foundPack = true
			if !bytes.Contains(payload, []byte("unpack ok")) {
				t.Errorf("channel-1 payload missing unpack status")
			}
			// The inner flush "0000" must be intact inside this payload.
			if !bytes.HasSuffix(payload, []byte("0000")) {
				t.Errorf("channel-1 payload missing inner flush: %x", payload)
			}
		case 2:
			foundProgress = true
			// The first progress line carries contribution_id; the second
			// is a "Next:" hint. Check that at least one line has it.
			if bytes.Contains(payload, []byte("contribution_id: 42")) {
				foundProgress = true
			}
		default:
			t.Errorf("unexpected channel %d", ch)
		}
		pos += pl
	}
	if pos != len(out) {
		t.Errorf("trailing bytes after flush: %d", len(out)-pos)
	}
	if !foundPack {
		t.Error("missing channel-1 (pack data) frame")
	}
	if !foundProgress {
		t.Error("missing channel-2 (progress) frame with contribution hints")
	}
}

func TestLastOuterFlush(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		wantPos int
	}{
		{
			name: "normal double-framed",
			// Simulates: <sideband-1 ...inner with flush...> + 0000
			raw: func() []byte {
				var b bytes.Buffer
				inner := "000eunpack ok\n" + "0018ok refs/heads/main\n" + "0000"
				payload := "\x01" + inner
				fmt.Fprintf(&b, "%04x%s", 4+len(payload), payload)
				b.WriteString("0000")
				return b.Bytes()
			}(),
			wantPos: 46, // position of the outer "0000"
		},
		{
			name: "no outer flush (truncated)",
			// Simulates receive-pack crash: only sideband-1, no outer 0000
			raw: func() []byte {
				var b bytes.Buffer
				inner := "000eunpack ok\n" + "0018ok refs/heads/main\n" + "0000"
				payload := "\x01" + inner
				fmt.Fprintf(&b, "%04x%s", 4+len(payload), payload)
				return b.Bytes()
			}(),
			wantPos: -1, // no standalone flush
		},
		{
			name:    "empty",
			raw:     []byte{},
			wantPos: -1,
		},
		{
			name:    "only flush",
			raw:     []byte("0000"),
			wantPos: 0,
		},
		{
			name:    "flush then truncated pkt",
			raw:     []byte("0000" + "0011\x01partial"),
			wantPos: 0, // the first 0000 is standalone
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lastOuterFlush(tt.raw)
			if got != tt.wantPos {
				t.Errorf("lastOuterFlush = %d, want %d", got, tt.wantPos)
			}
		})
	}
}

func TestSidebandPktLine(t *testing.T) {
	line := sidebandPktLine("hello")
	// Format: <4-hex-len><0x02>hello\n
	if len(line) < 4+1+5+1 {
		t.Fatalf("too short: %x", line)
	}
	var pl int
	if _, err := fmt.Sscanf(string(line[:4]), "%04x", &pl); err != nil {
		t.Fatalf("bad length prefix: %x", line[:4])
	}
	if pl != len(line) {
		t.Errorf("prefix %04x != actual length %d", pl, len(line))
	}
	if line[4] != 2 {
		t.Errorf("channel byte: want 0x02, got 0x%02x", line[4])
	}
	payload := string(line[5:])
	if !strings.HasSuffix(payload, "\n") {
		t.Error("payload must end with newline")
	}
	if !strings.Contains(payload, "hello") {
		t.Error("payload missing text")
	}
}

func TestSidebandPktLine_Overflow(t *testing.T) {
	// Generate text that would exceed the pkt-line max (0xfff0).
	big := strings.Repeat("x", 0xfff0)
	line := sidebandPktLine(big)
	if len(line) > 0xfff0 {
		t.Errorf("pkt-line exceeds protocol max: %d > %d", len(line), 0xfff0)
	}
	// Must still be valid.
	var pl int
	if _, err := fmt.Sscanf(string(line[:4]), "%04x", &pl); err != nil {
		t.Fatalf("overflow output has bad prefix: %x", line[:4])
	}
	if pl != len(line) {
		t.Errorf("overflow prefix %04x != actual %d", pl, len(line))
	}
}
