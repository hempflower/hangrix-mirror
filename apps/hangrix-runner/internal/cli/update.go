package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/store"
)

// Update self-replaces the running hangrix-runner with the build the
// server is embedding for our (GOOS, GOARCH).
//
// Flow:
//  1. Load state.json — we need the long-term agent token to authenticate
//     to /api/runner/bootstrap.
//  2. Fetch a fresh bootstrap and pick the binaryInfo entry for our own
//     runtime triple (asset `hangrix-runner_<goos>_<goarch>`).
//  3. Compare the advertised SHA256 to the on-disk binary's SHA256. If
//     they already match and --force was not set, exit clean.
//  4. GET the asset (bearer-authed), verify the body SHA256 matches the
//     bootstrap-declared SHA, and atomically rename it into place via a
//     tmp file in the same directory.
//
// Update never restarts the runner — that's the operator / service
// manager's job. We print the affected path and the SHA transition so a
// systemd unit's ExecStartPre can pick up the new bytes on the next boot.
func Update(ctx context.Context, cfg *config.Config) error {
	state, err := store.Load(cfg.StateDir)
	if err != nil {
		return err
	}
	cli := client.New(state.Server).WithAgentToken(state.AgentToken)

	boot, err := cli.Bootstrap(ctx)
	if err != nil {
		return fmt.Errorf("refresh bootstrap: %w", err)
	}

	res, err := runUpdate(ctx, cli, boot, cfg.Force)
	if err != nil {
		return err
	}
	if res.updated {
		fmt.Fprintf(os.Stdout, "updated hangrix-runner at %s: %s → %s\n", res.path, short(res.oldSHA), short(res.newSHA))
		fmt.Fprintln(os.Stdout, "restart the runner service to pick up the new binary.")
	} else {
		fmt.Fprintf(os.Stdout, "hangrix-runner already up to date (sha256 %s) at %s\n", short(res.oldSHA), res.path)
	}
	return nil
}

// updateResult captures what runUpdate did so its two callers can decide
// how to surface it. `updated == false` means the on-disk binary already
// matched the server's advertised SHA (or matched after force=false's
// short-circuit); oldSHA/newSHA carry the transition for logging.
type updateResult struct {
	updated bool
	path    string
	oldSHA  string
	newSHA  string
}

// runUpdate executes the SHA-compare → download → verify → swap pipeline
// against a pre-fetched bootstrap. Split out so `serve --auto-update` can
// reuse the bootstrap it already pulled at startup instead of paying for
// a second round trip on every boot.
func runUpdate(ctx context.Context, cli *client.Client, boot *client.BootstrapPayload, force bool) (updateResult, error) {
	asset := fmt.Sprintf("hangrix-runner_%s_%s", runtime.GOOS, runtime.GOARCH)
	info, ok := boot.Binaries[asset]
	if !ok || info.URL == "" {
		return updateResult{}, fmt.Errorf("server has no runner build for %s/%s (asset %q)", runtime.GOOS, runtime.GOARCH, asset)
	}
	if info.SHA256 == "" {
		return updateResult{}, fmt.Errorf("server returned binary %s with empty sha256 — refusing to install untrusted bytes", asset)
	}

	exe, err := selfPath()
	if err != nil {
		return updateResult{}, fmt.Errorf("locate current binary: %w", err)
	}
	curSHA, err := fileSHA256(exe)
	if err != nil {
		return updateResult{}, fmt.Errorf("hash current binary %s: %w", exe, err)
	}
	if curSHA == info.SHA256 && !force {
		return updateResult{path: exe, oldSHA: curSHA, newSHA: curSHA}, nil
	}

	body, srvSHA, err := cli.DownloadBinary(ctx, info.URL)
	if err != nil {
		return updateResult{}, fmt.Errorf("download %s: %w", asset, err)
	}
	if len(body) == 0 {
		return updateResult{}, fmt.Errorf("download %s: empty body", asset)
	}
	gotSum := sha256.Sum256(body)
	got := hex.EncodeToString(gotSum[:])
	if got != info.SHA256 {
		return updateResult{}, fmt.Errorf("downloaded %s sha256 mismatch: got %s want %s (bootstrap)", asset, got, info.SHA256)
	}
	// Header is best-effort — the binary handler sets it but we only
	// hard-fail when it disagrees with what we just hashed.
	if srvSHA != "" && srvSHA != got {
		return updateResult{}, fmt.Errorf("downloaded %s sha256 mismatch: got %s want %s (X-Hangrix-SHA256)", asset, got, srvSHA)
	}

	if err := swapBinary(exe, body); err != nil {
		return updateResult{}, fmt.Errorf("install %s: %w", exe, err)
	}
	return updateResult{updated: true, path: exe, oldSHA: curSHA, newSHA: got}, nil
}

// testSelfPathEnv lets the unit test point Update at a throwaway file
// instead of the real go-test binary. Only honoured when set, so it
// can't be triggered accidentally in production — there's no flag for
// it on the CLI surface.
const testSelfPathEnv = "HANGRIX_RUNNER_UPDATE_SELF_PATH"

// selfPath returns the canonical path to the running binary, resolving
// symlinks so the atomic rename hits the real file rather than replacing
// a /usr/local/bin → /opt/hangrix/bin symlink itself.
func selfPath() (string, error) {
	if override := os.Getenv(testSelfPathEnv); override != "" {
		return override, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// swapBinary writes the new bytes to a tmp file in the same directory
// (so rename stays on one filesystem) and renames it over the live
// binary. On Linux this works while the current process is still running
// — the kernel keeps the old inode pinned for the running PID; the new
// inode is what subsequent execs pick up.
func swapBinary(exe string, body []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".hangrix-runner.*")
	if err != nil {
		return fmt.Errorf("create tmp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		cleanup()
		return fmt.Errorf("rename %s → %s: %w", tmpPath, exe, err)
	}
	return nil
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
