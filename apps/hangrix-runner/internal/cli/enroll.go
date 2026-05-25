// Package cli implements the runner's three subcommands.
//
// enroll: trade an enroll plaintext for a long-lived agent token, then
//
//	snapshot the platform's bootstrap response (base URL, default
//	agent image, poll/heartbeat cadence) into state.json. After this
//	completes, `serve` needs no further flags.
//
// serve:  read state.json, refresh bootstrap (in case the platform
//
//	was reconfigured), extract the embedded `hangrix-agent` binary to
//	disk, then run the heartbeat + task-poll loop until SIGINT/SIGTERM.
//
// update: read state.json, refresh bootstrap, look up the server's
//
//	embedded `hangrix-runner_<goos>_<goarch>` artefact for this host,
//	and atomically replace the running binary on disk when the SHA
//	differs (or always when --force is set). Restart is the operator's
//	responsibility — `update` only writes bytes.
//
// The agent binary used to come down over /api/runner/binaries; it
// now rides inside the runner via the agentbin package, so there's no
// network round-trip on the bootstrap path for it. The runner binary
// itself still ships from the server because the runner can't embed a
// copy of itself.
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

	state := store.State{
		Server:     cfg.Server,
		RunnerID:   out.RunnerID,
		RunnerName: out.RunnerName,
		AgentToken: out.AgentToken,
	}
	applyBootstrap(&state, &out.Bootstrap)
	if err := store.Save(cfg.StateDir, &state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	fmt.Fprintf(os.Stdout, "enrolled runner %d (%q); state written to %s\n",
		out.RunnerID, out.RunnerName, cfg.StateDir)
	return nil
}

// applyBootstrap merges a fresh bootstrap payload into the runner
// state. Used by both enroll (first-time) and serve (refresh on
// startup). Pure metadata — no I/O, no downloads.
func applyBootstrap(state *store.State, b *client.BootstrapPayload) {
	state.BaseURL = b.BaseURL
	state.DefaultAgentImage = b.DefaultAgentImage
	state.PollWaitSec = b.PollWaitSec
	state.HeartbeatSec = b.HeartbeatSec
}
