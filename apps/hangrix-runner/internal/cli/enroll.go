// Package cli implements the runner's two subcommands.
//
// enroll: trade an enroll plaintext for a long-lived agent token, then
//   download the platform-advertised binaries (hangrix-agent today,
//   hangrix-runner for self-update) and snapshot endpoints + image
//   defaults into state.json. After this completes, `serve` needs no
//   further flags.
//
// serve:  read state.json, refresh bootstrap (in case the platform was
//   reconfigured), ensure cached binaries match the advertised shas
//   (re-download if not), then run the heartbeat + task-poll loop until
//   SIGINT/SIGTERM.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/store"
)

// agentBinaryName is the embed key the platform uses for the in-container
// agent. Mirrors apps/hangrix/.../binaries.NameAgent — kept as a literal
// here so the runner has no transitive dep on the server binaries pkg.
const agentBinaryName = "hangrix-agent"

func Enroll(ctx context.Context, cfg *config.Config) error {
	cli := client.New(cfg.Server)
	caps, _ := json.Marshal(map[string]any{
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
		"go":      runtime.Version(),
		"runtime": "docker",
	})
	out, err := cli.Enroll(ctx, client.EnrollRequest{
		EnrollToken:  cfg.EnrollToken,
		Capabilities: caps,
	})
	if err != nil {
		return fmt.Errorf("enroll: %w", err)
	}
	cli = cli.WithAgentToken(out.AgentToken)

	state := store.State{
		Server:     cfg.Server,
		RunnerID:   out.RunnerID,
		RunnerName: out.RunnerName,
		AgentToken: out.AgentToken,
	}
	if err := applyBootstrap(ctx, cli, cfg.StateDir, &state, &out.Bootstrap); err != nil {
		return err
	}
	if err := store.Save(cfg.StateDir, &state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	fmt.Fprintf(os.Stdout, "enrolled runner %d (%q); state written to %s\n",
		out.RunnerID, out.RunnerName, cfg.StateDir)
	return nil
}

// applyBootstrap merges a fresh bootstrap payload into the runner state
// and downloads any binaries whose sha changed (or were never cached).
// Used by both enroll (first-time) and serve (refresh on startup).
func applyBootstrap(ctx context.Context, cli *client.Client, stateDir string, state *store.State, b *client.BootstrapPayload) error {
	state.BaseURL = b.BaseURL
	state.DefaultAgentImage = b.DefaultAgentImage
	state.PollWaitSec = b.PollWaitSec
	state.HeartbeatSec = b.HeartbeatSec

	if len(b.Binaries) == 0 {
		return fmt.Errorf("platform bootstrap returned no binaries (server build missing payload/)")
	}
	if _, ok := b.Binaries[agentBinaryName]; !ok {
		return fmt.Errorf("platform bootstrap missing %q binary", agentBinaryName)
	}
	if state.Binaries == nil {
		state.Binaries = map[string]store.BinaryEntry{}
	}
	for name, info := range b.Binaries {
		entry, err := ensureBinary(ctx, cli, stateDir, name, info)
		if err != nil {
			return fmt.Errorf("ensure %s: %w", name, err)
		}
		state.Binaries[name] = entry
	}
	return nil
}

// ensureBinary downloads a single artefact into the content-addressed
// cache at <state-dir>/agent-binaries/<sha>. Idempotent: if the file
// already exists at the expected sha + size the call is a no-op.
func ensureBinary(ctx context.Context, cli *client.Client, stateDir, name string, info client.BinaryInfo) (store.BinaryEntry, error) {
	if info.SHA256 == "" {
		return store.BinaryEntry{}, fmt.Errorf("server did not advertise sha256")
	}
	dir := store.AgentBinariesDir(stateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return store.BinaryEntry{}, err
	}
	dst := store.AgentBinaryPathFor(stateDir, info.SHA256)
	// Cache hit: existing regular file with the expected size.
	// `IsRegular` matters because Docker silently creates an empty
	// directory at the bind-mount source when the file is missing —
	// re-running enroll under that state must overwrite, not accept
	// the directory.
	if stat, err := os.Stat(dst); err == nil && stat.Mode().IsRegular() && (info.Size == 0 || stat.Size() == info.Size) {
		return store.BinaryEntry{URL: info.URL, SHA256: info.SHA256, Size: info.Size, LocalPath: dst}, nil
	}
	// If dst exists but isn't a regular file (most often: a stray
	// directory Docker materialised), clear it so the rename below
	// can land the real binary.
	if stat, err := os.Stat(dst); err == nil && !stat.Mode().IsRegular() {
		if err := os.RemoveAll(dst); err != nil {
			return store.BinaryEntry{}, fmt.Errorf("clear stale cache at %s: %w", dst, err)
		}
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return store.BinaryEntry{}, err
	}
	written, err := cli.DownloadBinary(ctx, info.URL, info.SHA256, f)
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return store.BinaryEntry{}, err
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return store.BinaryEntry{}, closeErr
	}
	if info.Size > 0 && written != info.Size {
		_ = os.Remove(tmp)
		return store.BinaryEntry{}, fmt.Errorf("short read: want %d bytes got %d", info.Size, written)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return store.BinaryEntry{}, err
	}
	return store.BinaryEntry{URL: info.URL, SHA256: info.SHA256, Size: info.Size, LocalPath: dst}, nil
}
