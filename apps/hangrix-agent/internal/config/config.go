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

	// LLMMaxContextTokens is the max_context_tokens from agents.yml,
	// surfaced by the runner as HANGRIX_LLM_MAX_CONTEXT_TOKENS. When
	// non-zero, the default CompactTokenThreshold is 80% of this value.
	// Zero means "not configured" — the default falls back to 80000.
	LLMMaxContextTokens int

	// CompactTokenThreshold is the input-token usage above which the
	// runtime nudges the LLM (via a synthetic system reminder injected
	// at the next turn boundary) to call compact_session. 0 disables the
	// nudge — the LLM still decides on its own when to compact.
	// Set explicitly via HANGRIX_COMPACT_TOKEN_THRESHOLD; when unset,
	// defaults to 80% of LLMMaxContextTokens (or 80000 if that
	// is also unset). A negative value disables the nudge.
	CompactTokenThreshold int

	// LLMReasoningTimeoutSeconds is the per-call wall-clock ceiling the
	// runtime enforces on a single Create() invocation. When exceeded the
	// agent cancels the HTTP request and — if retries remain — retries
	// with the same request snapshot. <=0 disables the protection (the
	// call falls through to the http.Client's 5-minute timeout). Set via
	// HANGRIX_LLM_REASONING_TIMEOUT_SECONDS; default 200.
	LLMReasoningTimeoutSeconds int
	// LLMReasoningTimeoutRetries is the number of retries after the first
	// timeout. Default 1 means 2 total attempts. Only reasoning-timeout
	// errors are retried at this level; transport/5xx/429 retries stay
	// inside llm.Client.Create. Set via HANGRIX_LLM_REASONING_TIMEOUT_RETRIES.
	// Clamped to >=0 to prevent negative values from zeroing maxAttempts.
	LLMReasoningTimeoutRetries int
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
	maxCtx := parseMaxContextTokens(os.Getenv("HANGRIX_LLM_MAX_CONTEXT_TOKENS"))
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
			LLMMaxContextTokens:    maxCtx,
			CompactTokenThreshold:      parseCompactThreshold(os.Getenv("HANGRIX_COMPACT_TOKEN_THRESHOLD"), maxCtx),
			LLMReasoningTimeoutSeconds: parseIntDefault(os.Getenv("HANGRIX_LLM_REASONING_TIMEOUT_SECONDS"), 200),
			LLMReasoningTimeoutRetries: clampNonNegative(parseIntDefault(os.Getenv("HANGRIX_LLM_REASONING_TIMEOUT_RETRIES"), 1)),
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


// parseMaxContextTokens reads HANGRIX_LLM_MAX_CONTEXT_TOKENS. Returns 0
// when unset or unparseable — the caller treats 0 as "not configured".
func parseMaxContextTokens(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// defaultCompactThreshold returns 80% of maxCtx when maxCtx > 0,
// otherwise falls back to 80000.
func defaultCompactThreshold(maxCtx int) int {
	if maxCtx > 0 {
		return maxCtx * 80 / 100
	}
	return 80000
}

// parseCompactThreshold reads HANGRIX_COMPACT_TOKEN_THRESHOLD. When
// explicitly set, returns that value (negative disables). When unset,
// defaults to 80% of maxCtx (from LLMMaxContextTokens), or 80000 if maxCtx is zero.
// A negative explicit value disables the nudge entirely.
func parseCompactThreshold(raw string, maxCtx int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultCompactThreshold(maxCtx)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultCompactThreshold(maxCtx)
	}
	if n < 0 {
		return 0
	}
	return n
}

// parseIntDefault reads an env value as an int, falling back to def when
// empty or unparseable. Used for simple count/duration env vars that
// have a sensible default.
func parseIntDefault(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// clampNonNegative floors the value to 0 when negative. This guards
// against misconfiguration (e.g. HANGRIX_LLM_REASONING_TIMEOUT_RETRIES=-1)
// that would otherwise zero out maxAttempts and skip the LLM call entirely.
func clampNonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
