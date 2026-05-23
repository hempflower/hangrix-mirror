package cli

import (
	"context"
	"errors"
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

// autoUpdateInterval is how often `serve --auto-update` re-checks the
// server's embedded runner build while serving. One minute is the
// trade-off: fast enough to roll out a fix across a fleet within a
// release window, slow enough that we're not hammering /bootstrap.
const autoUpdateInterval = time.Minute

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

	// Auto-update path: if the server's embedded runner build differs
	// from ours, swap it in and exit cleanly so the supervisor restarts
	// onto the new bytes. We share the bootstrap we just fetched, so
	// there's no extra round trip on the steady-state "no update" path.
	// Failures are logged but non-fatal — a broken update endpoint
	// shouldn't keep a healthy runner from serving.
	if cfg.AutoUpdate {
		res, err := runUpdate(ctx, cli, boot, false)
		if err != nil {
			log.Printf("auto-update: %v (continuing with current binary)", err)
		} else if res.updated {
			log.Printf("auto-update: installed new binary at %s (%s → %s); exiting for supervisor restart",
				res.path, short(res.oldSHA), short(res.newSHA))
			return nil
		}
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
		Parallelism:     cfg.Parallelism,
	}

	// In --mock mode, wire a LocalOrchestrator for agent sessions only.
	// Workflow jobs continue to use the DockerOrchestrator via
	// l.Orchestrator; only l.SessionOrchestrator is overridden so the
	// WorkflowJobDriver path is never touched.
	if cfg.Mock {
		l.SessionOrchestrator = orchestrator.NewLocal()
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Periodic auto-update: while the loop runs, re-check the server's
	// embedded build every autoUpdateInterval. On a successful install
	// we cancel the serve context so the loop drains and Serve returns
	// nil — the operator's supervisor then restarts onto the new bytes.
	if cfg.AutoUpdate {
		go autoUpdateLoop(ctx, cli, cancel)
	}

	log.Printf("runner %d (%q) serving against %s", state.RunnerID, state.RunnerName, state.Server)
	return l.Run(ctx)
}

// autoUpdateLoop polls /bootstrap on a fixed cadence and runs the same
// SHA-compare → download → verify → swap pipeline as the startup check.
// Bootstrap is refetched each tick so a server-side build promoted after
// the runner started is picked up without an enroll cycle. Errors are
// logged and the loop keeps ticking — a transient platform outage must
// not knock the runner offline.
func autoUpdateLoop(ctx context.Context, cli *client.Client, onUpdate func()) {
	tick := time.NewTicker(autoUpdateInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		boot, err := cli.Bootstrap(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("auto-update: refresh bootstrap: %v", err)
			}
			continue
		}
		res, err := runUpdate(ctx, cli, boot, false)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("auto-update: %v (continuing with current binary)", err)
			}
			continue
		}
		if res.updated {
			log.Printf("auto-update: installed new binary at %s (%s → %s); shutting down for supervisor restart",
				res.path, short(res.oldSHA), short(res.newSHA))
			onUpdate()
			return
		}
	}
}
