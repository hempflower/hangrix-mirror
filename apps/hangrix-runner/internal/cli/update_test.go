package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/store"
)

// TestUpdateReplacesBinary stands up a fake platform that serves a
// pretend hangrix-runner build for the test runtime's GOOS/GOARCH, then
// asks `Update` to replace a stub binary in a temp dir with it. The test
// exercises:
//   - state load
//   - bootstrap fetch + asset lookup
//   - SHA check (mismatch → install path)
//   - DownloadBinary call with bearer auth
//   - swapBinary atomic rename
func TestUpdateReplacesBinary(t *testing.T) {
	// No t.Parallel — uses t.Setenv to redirect selfPath().
	const agentToken = "hgxa_test_token"
	newBytes := []byte("fake-runner-binary-payload\n")
	newSum := sha256.Sum256(newBytes)
	newSHA := hex.EncodeToString(newSum[:])
	asset := fmt.Sprintf("hangrix-runner_%s_%s", runtime.GOOS, runtime.GOARCH)
	binaryPath := "/api/runner/binaries/" + asset

	var bootstrapHits, downloadHits int
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+agentToken {
			http.Error(w, "missing/wrong auth: "+got, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/runner/bootstrap":
			bootstrapHits++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"base_url": "http://platform.example",
				"binaries": map[string]any{
					asset: map[string]any{
						"url":    binaryPath,
						"name":   "hangrix-runner",
						"goos":   runtime.GOOS,
						"goarch": runtime.GOARCH,
						"sha256": newSHA,
						"size":   len(newBytes),
					},
				},
				"poll_wait_sec": 25,
				"heartbeat_sec": 20,
			})
		case binaryPath:
			downloadHits++
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Hangrix-SHA256", newSHA)
			_, _ = w.Write(newBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer platform.Close()

	stateDir := t.TempDir()
	if err := store.Save(stateDir, &store.State{
		Server:     platform.URL,
		RunnerID:   42,
		RunnerName: "test-runner",
		AgentToken: agentToken,
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	// Stand up a stub "binary" in a temp dir and aim Update at it via
	// the HANGRIX_TEST_SELF_PATH override. This decouples the test from
	// the real go-test binary, which we obviously don't want to swap.
	binDir := t.TempDir()
	exe := filepath.Join(binDir, "hangrix-runner")
	oldBytes := []byte("old-runner-binary\n")
	if err := os.WriteFile(exe, oldBytes, 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}
	t.Setenv(testSelfPathEnv, exe)

	cfg := &config.Config{StateDir: stateDir}
	if err := Update(context.Background(), cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read replaced binary: %v", err)
	}
	if string(got) != string(newBytes) {
		t.Fatalf("binary not replaced: got %q want %q", got, newBytes)
	}
	if bootstrapHits != 1 {
		t.Fatalf("bootstrap hits: got %d want 1", bootstrapHits)
	}
	if downloadHits != 1 {
		t.Fatalf("download hits: got %d want 1", downloadHits)
	}

	// Second run with the same server-side SHA must be a no-op: no
	// download, but bootstrap is still hit (Update always refreshes so
	// it can see a server rollback).
	if err := Update(context.Background(), cfg); err != nil {
		t.Fatalf("Update idempotent run: %v", err)
	}
	if bootstrapHits != 2 {
		t.Fatalf("second bootstrap hits: got %d want 2", bootstrapHits)
	}
	if downloadHits != 1 {
		t.Fatalf("second download hits: got %d want 1 (should not redownload)", downloadHits)
	}

	// --force makes it redownload even on SHA match.
	cfg.Force = true
	if err := Update(context.Background(), cfg); err != nil {
		t.Fatalf("Update --force: %v", err)
	}
	if downloadHits != 2 {
		t.Fatalf("forced download hits: got %d want 2", downloadHits)
	}
}

// TestUpdateRejectsSHAMismatch verifies that bytes whose digest doesn't
// match the bootstrap claim are refused. This is the only line of
// defense against a tampered binary endpoint: bootstrap auth proves the
// payload metadata came from the platform; the body hash check proves
// the bytes we're about to install are those metadata's bytes.
func TestUpdateRejectsSHAMismatch(t *testing.T) {
	// No t.Parallel — uses t.Setenv to redirect selfPath().
	const agentToken = "hgxa_test_token"
	asset := fmt.Sprintf("hangrix-runner_%s_%s", runtime.GOOS, runtime.GOARCH)
	binaryPath := "/api/runner/binaries/" + asset
	claimedSHA := strings.Repeat("0", 64)

	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runner/bootstrap":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"binaries": map[string]any{
					asset: map[string]any{
						"url":    binaryPath,
						"sha256": claimedSHA,
					},
				},
			})
		case binaryPath:
			_, _ = w.Write([]byte("totally-different-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer platform.Close()

	stateDir := t.TempDir()
	if err := store.Save(stateDir, &store.State{
		Server: platform.URL, RunnerID: 1, RunnerName: "x", AgentToken: agentToken,
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	binDir := t.TempDir()
	exe := filepath.Join(binDir, "hangrix-runner")
	if err := os.WriteFile(exe, []byte("untouched"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv(testSelfPathEnv, exe)

	err := Update(context.Background(), &config.Config{StateDir: stateDir})
	if err == nil {
		t.Fatalf("expected SHA mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(exe)
	if string(got) != "untouched" {
		t.Fatalf("binary should not have been overwritten on SHA mismatch; got %q", got)
	}
}

// TestSwapBinaryAtomic exercises the install path on its own — a tmp
// file in the destination directory, atomic rename, 0755 perm bit.
func TestSwapBinaryAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := filepath.Join(dir, "hangrix-runner")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := swapBinary(exe, []byte("v2-and-then-some")); err != nil {
		t.Fatalf("swap: %v", err)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "v2-and-then-some" {
		t.Fatalf("body: got %q want %q", got, "v2-and-then-some")
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o755 {
		t.Fatalf("mode: got %o want 0755", mode)
	}
	// No leftover .hangrix-runner.* tmp files in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hangrix-runner.") {
			t.Fatalf("tmp file leaked: %s", e.Name())
		}
	}
}
