package cli

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/loop"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/store"
)

// Serve reads state.json, re-fetches the bootstrap so the runner picks
// up server-side config changes since enroll, then runs the loop. The
// only flag accepted is --state-dir; everything else comes off state.
func Serve(ctx context.Context, cfg *config.Config) error {
	state, err := store.Load(cfg.StateDir)
	if err != nil {
		return err
	}

	cli := client.New(state.Server).WithAgentToken(state.AgentToken)

	// Refresh bootstrap so endpoint / image / binary sha changes
	// propagate without re-enrolling.
	boot, err := cli.Bootstrap(ctx)
	if err != nil {
		return fmt.Errorf("refresh bootstrap: %w", err)
	}
	if err := applyBootstrap(ctx, cli, cfg.StateDir, state, boot); err != nil {
		return fmt.Errorf("apply bootstrap: %w", err)
	}
	if err := store.Save(cfg.StateDir, state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	agent, ok := state.Binaries[agentBinaryName]
	if !ok || agent.LocalPath == "" {
		return fmt.Errorf("agent binary not cached (try `hangrix-runner enroll` again)")
	}

	orch := orchestrator.NewDocker(cfg.DockerBin)
	hb := time.Duration(state.HeartbeatSec) * time.Second
	if hb <= 0 {
		hb = 20 * time.Second
	}

	l := &loop.Loop{
		Client:          cli,
		Orchestrator:    orch,
		AgentBinaryPath: agent.LocalPath,
		WorkspaceRoot:   state.LocalWorkspaceDir(cfg.StateDir),
		LLMEndpoint:     state.LLMEndpoint,
		MCPEndpoint:     state.MCPEndpoint,
		HeartbeatEvery:  hb,
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	log.Printf("runner %d (%q) serving against %s", state.RunnerID, state.RunnerName, state.Server)
	return l.Run(ctx)
}
