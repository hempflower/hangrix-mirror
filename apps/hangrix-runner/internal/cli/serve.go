package cli

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/bundles"
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

	// Content-addressed agent-bundle cache (M7a). Lives next to the
	// agent binary cache so a single state-dir tree captures everything
	// the runner needs to remount after a restart.
	bundleCache, err := bundles.New(bundles.Config{
		Root: filepath.Join(cfg.StateDir, "agent-bundles"),
	}, &bundles.HTTPFetcher{
		Base:       state.Server,
		AgentToken: state.AgentToken,
		HTTP:       &http.Client{Timeout: 5 * time.Minute},
	})
	if err != nil {
		return fmt.Errorf("init bundle cache: %w", err)
	}

	l := &loop.Loop{
		Client:          cli,
		Orchestrator:    orch,
		Bundles:         bundleCache,
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
