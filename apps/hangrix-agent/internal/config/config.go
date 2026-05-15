// Package config owns the agent's startup configuration: every HANGRIX_*
// environment variable the process reads, validated up-front so a typo or
// missing required value surfaces as one structured error before any
// component tries to use it.
//
// The Config struct is shared via the ioc container — other modules
// (llmclient, toolregistry, systemprompt, …) declare a *Config field on
// their Deps struct and read whatever subset they need.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config captures every HANGRIX_* the agent reads at startup. Fields are
// plain strings (no env access at read time) so consumers can rely on a
// stable value for the lifetime of the process and tests can construct a
// Config without touching os.Getenv.
type Config struct {
	SessionToken     string
	LLMEndpoint      string
	Model            string
	MCPEndpoint      string
	SessionID        string
	Role             string
	HostRepo         string
	IssueNumber      string
	WorkingBranch    string
	BaseBranch       string
	BundleDir        string
	HostAddendumPath string
	ToolCatalog      string
}

// NewConfig is the ioc-shaped provider: zero parameters, returns *Config.
// Missing-required values panic with one consolidated message so the
// runner sees a single line on stderr rather than a cascade of nil
// dereferences when downstream code reaches for an empty endpoint.
func NewConfig() *Config {
	cfg := &Config{
		SessionToken:     os.Getenv("HANGRIX_SESSION_TOKEN"),
		LLMEndpoint:      os.Getenv("HANGRIX_LLM_ENDPOINT"),
		Model:            os.Getenv("HANGRIX_LLM_MODEL"),
		MCPEndpoint:      os.Getenv("HANGRIX_PLATFORM_MCP_ENDPOINT"),
		SessionID:        os.Getenv("HANGRIX_SESSION_ID"),
		Role:             os.Getenv("HANGRIX_ROLE"),
		HostRepo:         os.Getenv("HANGRIX_HOST_REPO"),
		IssueNumber:      os.Getenv("HANGRIX_ISSUE_NUMBER"),
		WorkingBranch:    os.Getenv("HANGRIX_WORKING_BRANCH"),
		BaseBranch:       os.Getenv("HANGRIX_BASE_BRANCH"),
		BundleDir:        os.Getenv("HANGRIX_AGENT_BUNDLE"),
		HostAddendumPath: os.Getenv("HANGRIX_HOST_ADDENDUM"),
		ToolCatalog:      os.Getenv("HANGRIX_TOOL_CATALOG"),
	}

	// MCP endpoint is intentionally optional: M6b smoke-test runs the
	// agent with local tools only and no platform connection.
	var missing []string
	if cfg.SessionToken == "" {
		missing = append(missing, "HANGRIX_SESSION_TOKEN")
	}
	if cfg.LLMEndpoint == "" {
		missing = append(missing, "HANGRIX_LLM_ENDPOINT")
	}
	if cfg.Model == "" {
		missing = append(missing, "HANGRIX_LLM_MODEL")
	}
	if len(missing) > 0 {
		panic(fmt.Errorf("config: missing required env: %s", strings.Join(missing, ", ")))
	}
	return cfg
}
