package cli

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/agentbin"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/loop"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/store"
)

// Serve reads state.json, re-fetches the bootstrap so the runner picks
// up server-side config changes since enroll, extracts the embedded
// agent to disk, then runs the loop. The only flag accepted is
// --state-dir; everything else comes off state.
func Serve(ctx context.Context, cfg *config.Config) error {
	state, err := store.Load(cfg.StateDir)
	if err != nil {
		return err
	}

	cli := client.New(state.Server).WithAgentToken(state.AgentToken)

	// Refresh bootstrap so endpoint / image / cadence changes
	// propagate without re-enrolling.
	boot, err := cli.Bootstrap(ctx)
	if err != nil {
		return fmt.Errorf("refresh bootstrap: %w", err)
	}
	applyBootstrap(state, boot)
	if err := store.Save(cfg.StateDir, state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Extract the agent binary we shipped with into a stable path.
	// agentbin.Extract is idempotent — fast path is "file already
	// there, sha matches, no disk write".
	agentPath, err := agentbin.Extract(filepath.Join(cfg.StateDir, "agent"))
	if err != nil {
		return fmt.Errorf("extract embedded agent: %w", err)
	}

	orch := orchestrator.NewDocker(cfg.DockerBin)
	hb := time.Duration(state.HeartbeatSec) * time.Second
	if hb <= 0 {
		hb = 20 * time.Second
	}

	l := &loop.Loop{
		Client:          cli,
		Orchestrator:    orch,
		AgentBinaryPath: agentPath,
		WorkspaceRoot:   state.LocalWorkspaceDir(cfg.StateDir),
		BaseURL:         state.BaseURL,
		HeartbeatEvery:  hb,
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	log.Printf("runner %d (%q) serving against %s", state.RunnerID, state.RunnerName, state.Server)
	return l.Run(ctx)
}
