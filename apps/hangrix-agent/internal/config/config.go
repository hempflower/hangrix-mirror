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
	"strconv"
	"strings"
)

// Config captures every HANGRIX_* the agent reads at startup. Fields are
// plain strings (no env access at read time) so consumers can rely on a
// stable value for the lifetime of the process and tests can construct a
// Config without touching os.Getenv.
//
// PlatformBaseURL is the one network anchor the agent needs. Both the
// LLM proxy (`<base>/api/llm/v1/responses`) and the platform tool
// endpoints (`<base>/api/agent/tools/<name>`) are derived from it by
// the llm and tools/platform modules respectively.
type Config struct {
	SessionToken     string
	PlatformBaseURL  string
	Model            string
	SessionID        string
	Role             string
	HostRepo         string
	IssueNumber      string
	WorkingBranch    string
	BaseBranch       string
	HostAddendumPath string
	ToolCatalog      string
	McpServers       []string

	// CompactTokenThreshold is the input-token usage above which the
	// runtime nudges the LLM (via a synthetic system reminder injected
	// at the next turn boundary) to call compact_session. 0 disables the
	// nudge — the LLM still decides on its own when to compact. Set via
	// HANGRIX_COMPACT_TOKEN_THRESHOLD; default 80000 leaves headroom on
	// 128k-window models and is conservative enough for ~64k providers
	// (DeepSeek) when operators want to keep it on.
	CompactTokenThreshold int
}

// LLMEndpoint returns the URL the agent POSTs `/responses` against.
// Centralised here so the suffix lives next to its sibling
// PlatformToolsBaseURL — neither leaks into other modules.
func (c *Config) LLMEndpoint() string {
	if c.PlatformBaseURL == "" {
		return ""
	}
	return strings.TrimRight(c.PlatformBaseURL, "/") + "/api/llm/v1"
}

// PlatformToolsBaseURL returns the base the platform tool wrappers
// hit (one POST per tool: `<base>/<tool-name>`).
func (c *Config) PlatformToolsBaseURL() string {
	if c.PlatformBaseURL == "" {
		return ""
	}
	return strings.TrimRight(c.PlatformBaseURL, "/") + "/api/agent/tools"
}

// NewConfig is the ioc-shaped provider: zero parameters, returns *Config.
// Missing-required values panic with one consolidated message so the
// runner sees a single line on stderr rather than a cascade of nil
// dereferences when downstream code reaches for an empty endpoint.
func NewConfig() *Config {
	cfg := &Config{
		SessionToken:     os.Getenv("HANGRIX_SESSION_TOKEN"),
		PlatformBaseURL:  os.Getenv("HANGRIX_PLATFORM_BASE_URL"),
		Model:            os.Getenv("HANGRIX_LLM_MODEL"),
		SessionID:        os.Getenv("HANGRIX_SESSION_ID"),
		Role:             os.Getenv("HANGRIX_ROLE"),
		HostRepo:         os.Getenv("HANGRIX_HOST_REPO"),
		IssueNumber:      os.Getenv("HANGRIX_ISSUE_NUMBER"),
		WorkingBranch:    os.Getenv("HANGRIX_WORKING_BRANCH"),
		BaseBranch:       os.Getenv("HANGRIX_BASE_BRANCH"),
		HostAddendumPath:      os.Getenv("HANGRIX_HOST_ADDENDUM"),
		ToolCatalog:           os.Getenv("HANGRIX_TOOL_CATALOG"),
		McpServers:            parseMcpServers(os.Getenv("HANGRIX_MCP_SERVERS")),
		CompactTokenThreshold: parseCompactThreshold(os.Getenv("HANGRIX_COMPACT_TOKEN_THRESHOLD")),
	}

	var missing []string
	if cfg.SessionToken == "" {
		missing = append(missing, "HANGRIX_SESSION_TOKEN")
	}
	if cfg.PlatformBaseURL == "" {
		missing = append(missing, "HANGRIX_PLATFORM_BASE_URL")
	}
	if cfg.Model == "" {
		missing = append(missing, "HANGRIX_LLM_MODEL")
	}
	if len(missing) > 0 {
		panic(fmt.Errorf("config: missing required env: %s", strings.Join(missing, ", ")))
	}
	return cfg
}

// parseMcpServers splits a comma-separated env value into a slice of
// trimmed, non-empty server names. Empty input → nil (no servers).
func parseMcpServers(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseCompactThreshold reads HANGRIX_COMPACT_TOKEN_THRESHOLD. Empty or
// unparseable → default 80000 (rough 60% of a 128k window, conservative
// enough to also trigger on 64k providers). A negative value disables
// the nudge entirely so operators can opt out without removing the env.
func parseCompactThreshold(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 80000
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 80000
	}
	if n < 0 {
		return 0
	}
	return n
}
