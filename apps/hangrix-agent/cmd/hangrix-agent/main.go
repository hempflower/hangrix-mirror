// Command hangrix-agent is the long-running per-role agent process.
// It is bind-mounted into the runner's container by M6c, started with a
// curated env, and communicates with its runner over stdin/stdout
// JSON-Lines.
//
// Lifecycle:
//  1. Parse env (HANGRIX_*).
//  2. Build LLM and MCP clients from credentials in env.
//  3. Discover platform tools (tools/list) and merge with local tools.
//  4. Assemble the three-layer system prompt.
//  5. Run runtime.Loop until stdin closes or SIGTERM arrives.
//
// All initialisation errors are fatal — the runner observes the exit
// code and an error line on stderr, then decides whether to retry.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/mcp"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/prompt"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/runtime"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/tools/local"
)

func main() {
	if err := run(); err != nil {
		// stderr is dedicated to fatal init errors. Once the runtime is
		// running, all signal goes through the IPC writer instead.
		fmt.Fprintln(os.Stderr, "hangrix-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadEnv()
	if err != nil {
		return err
	}

	// SIGTERM / SIGINT → graceful: cancel the root context, the runtime
	// loop returns from its blocked stdin read once the shutdown
	// frame propagates (or the runner closes the pipe).
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	llmClient := llm.New(cfg.llmEndpoint, cfg.sessionToken)
	var mcpClient *mcp.Client
	if cfg.mcpEndpoint != "" {
		mcpClient = mcp.New(cfg.mcpEndpoint, cfg.sessionToken)
	}

	allow, err := tools.ParseToolCatalog(cfg.toolCatalog)
	if err != nil {
		return err
	}
	registry, err := tools.Build(ctx, local.All(), mcpClient, allow)
	if err != nil {
		return fmt.Errorf("tools: %w", err)
	}

	assembled, err := prompt.Assemble(prompt.Inputs{
		BundleDir:        cfg.bundleDir,
		HostAddendumPath: cfg.hostAddendumPath,
		Role:             cfg.role,
		HostRepo:         cfg.hostRepo,
		IssueNumber:      cfg.issueNumber,
		WorkingBranch:    cfg.workingBranch,
		BaseBranch:       cfg.baseBranch,
		SessionID:        cfg.sessionID,
		LLMEndpoint:      cfg.llmEndpoint,
		MCPEndpoint:      cfg.mcpEndpoint,
	})
	if err != nil {
		return err
	}

	in := ipc.NewReader(os.Stdin)
	out := ipc.NewWriter(os.Stdout)
	_ = out.Log("info", fmt.Sprintf("agent starting; system prompt layers: %v", assembled.KeptLayers))

	loop := runtime.NewLoop(in, out, llmClient, cfg.model, registry, assembled.Prompt)
	if err := loop.Run(ctx); err != nil {
		_ = out.Log("error", err.Error())
		return err
	}
	_ = out.Log("info", "agent stopping")
	return nil
}

// envConfig carries every HANGRIX_* the agent reads at startup. We
// validate up-front so misconfiguration surfaces as one error rather
// than a series of misleading downstream failures.
type envConfig struct {
	sessionToken     string
	llmEndpoint      string
	model            string
	mcpEndpoint      string
	sessionID        string
	role             string
	hostRepo         string
	issueNumber      string
	workingBranch    string
	baseBranch       string
	bundleDir        string
	hostAddendumPath string
	toolCatalog      string
}

func loadEnv() (*envConfig, error) {
	cfg := &envConfig{
		sessionToken:     os.Getenv("HANGRIX_SESSION_TOKEN"),
		llmEndpoint:      os.Getenv("HANGRIX_LLM_ENDPOINT"),
		model:            os.Getenv("HANGRIX_LLM_MODEL"),
		mcpEndpoint:      os.Getenv("HANGRIX_PLATFORM_MCP_ENDPOINT"),
		sessionID:        os.Getenv("HANGRIX_SESSION_ID"),
		role:             os.Getenv("HANGRIX_ROLE"),
		hostRepo:         os.Getenv("HANGRIX_HOST_REPO"),
		issueNumber:      os.Getenv("HANGRIX_ISSUE_NUMBER"),
		workingBranch:    os.Getenv("HANGRIX_WORKING_BRANCH"),
		baseBranch:       os.Getenv("HANGRIX_BASE_BRANCH"),
		bundleDir:        os.Getenv("HANGRIX_AGENT_BUNDLE"),
		hostAddendumPath: os.Getenv("HANGRIX_HOST_ADDENDUM"),
		toolCatalog:      os.Getenv("HANGRIX_TOOL_CATALOG"),
	}

	// Required envs. The MCP endpoint is intentionally optional: M6b
	// smoke-test runs the agent with local tools only.
	missing := []string{}
	if cfg.sessionToken == "" {
		missing = append(missing, "HANGRIX_SESSION_TOKEN")
	}
	if cfg.llmEndpoint == "" {
		missing = append(missing, "HANGRIX_LLM_ENDPOINT")
	}
	if cfg.model == "" {
		missing = append(missing, "HANGRIX_LLM_MODEL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env: %v", missing)
	}
	return cfg, nil
}
