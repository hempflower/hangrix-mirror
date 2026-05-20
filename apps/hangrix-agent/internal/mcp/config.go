// Package mcp loads MCP servers from the repo-root .mcp.json and exposes
// their tools as local.Tool instances that plug into the agent's unified
// tool catalogue.
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Config is the decoded .mcp.json at the workspace root.
type Config struct {
	McpServers map[string]ServerDef `json:"mcpServers"`
}

// ServerDef is one MCP server entry. The transport is inferred:
//
//   - If Command is non-empty: stdio (subprocess).
//   - If Type is "http": streamable HTTP.
//   - If Type is "sse": SSE.
type ServerDef struct {
	// Stdio
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`

	// Remote (http / sse)
	Type    string            `json:"type,omitempty"`    // "http" or "sse"
	URL     string            `json:"url,omitempty"`     // required for remote
	Headers map[string]string `json:"headers,omitempty"` // raw values; env expansion applied later
}

// Load reads and decodes .mcp.json from the workspace root. Returns
// (nil, nil) when the file does not exist — the caller treats that as
// "no additional MCP servers", which is not an error.
func Load(workspaceRoot string) (*Config, error) {
	path := workspaceRoot + "/.mcp.json"
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("mcp: read %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("mcp: parse %s: %w", path, err)
	}
	if len(cfg.McpServers) == 0 {
		return nil, nil
	}
	return &cfg, nil
}

// Validate checks required fields per transport. Returns the first error
// found; a valid config returns nil.
func (c *Config) Validate() error {
	for name, s := range c.McpServers {
		switch {
		case s.Command != "":
			// stdio — nothing else required.
		case s.Type == "http", s.Type == "sse":
			if s.URL == "" {
				return fmt.Errorf("mcp server %q: type %q requires url", name, s.Type)
			}
		default:
			return fmt.Errorf("mcp server %q: must have either command or type (\"http\" / \"sse\")", name)
		}
	}
	return nil
}

var envVarRe = regexp.MustCompile(`\$\{env:([^}]+)\}`)

// ExpandHeaders replaces ${env:VAR} references in every header value with
// the corresponding os.Getenv result. Returns an error naming the server
// and the first missing variable. Values that didn't contain any reference
// are passed through unchanged.
func ExpandHeaders(serverName string, headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		expanded, err := expandOne(serverName, v)
		if err != nil {
			return nil, err
		}
		out[k] = expanded
	}
	return out, nil
}

func expandOne(serverName, raw string) (string, error) {
	missing := envVarRe.FindAllStringSubmatch(raw, -1)
	if len(missing) == 0 {
		return raw, nil
	}
	replaced := raw
	for _, m := range missing {
		varName := m[1]
		val, ok := os.LookupEnv(varName)
		if !ok {
			return "", fmt.Errorf("mcp server %q: header references ${env:%s} which is not set", serverName, varName)
		}
		replaced = strings.Replace(replaced, m[0], val, 1)
	}
	return replaced, nil
}
