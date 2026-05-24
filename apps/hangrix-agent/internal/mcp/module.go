package mcp

import (
	"context"
	"fmt"
	"log"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/httpx"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	"github.com/hangrix/hangrix/pkg/ioc"
	mcpclient "github.com/mark3labs/mcp-go/client"
)

// Bundle pairs the MCP tools with the client slice (must be closed on shutdown).
type Bundle struct {
	Tools   []local.Tool
	Clients []*mcpclient.Client
}

// Close calls Close on every collected MCP client. Safe to call on a nil
// or empty Bundle. Must be called at agent shutdown to prevent stdio child
// process leaks and dangling HTTP/SSE connections.
func (b *Bundle) Close() {
	if b == nil {
		return
	}
	for _, c := range b.Clients {
		c.Close()
	}
}

// Deps is the ioc dependency set for NewBundle.
type Deps struct {
	Cfg *config.Config
}

// NewBundle loads .mcp.json from the workspace root, filters it to only the
// servers in the role's MCP whitelist (deps.Cfg.McpServers), and returns the
// connected tools + clients.
//
// When the whitelist is nil or empty, no MCP servers are loaded — the bundle
// is empty. This is the default: a role that doesn't declare mcp: in
// agents.yml gets no MCP tools.
//
// When the whitelist is non-empty and .mcp.json is missing, empty, or
// unparseable, the session panics (explicit failure) because this is a host
// configuration error, not a recoverable degradation.
//
// When the whitelist names a server that doesn't exist in .mcp.json, the
// session also panics for the same reason.
func NewBundle(deps *Deps) *Bundle {
	whitelist := deps.Cfg.McpServers
	// No whitelist → no MCP servers at all (default: role didn't declare mcp:).
	if len(whitelist) == 0 {
		return &Bundle{}
	}

	cfg, err := Load("/workspace")
	if err != nil {
		panic(fmt.Errorf("mcp: %v", err))
	}
	if cfg == nil {
		panic(fmt.Errorf("mcp: role declares MCP servers %v but .mcp.json is missing or has no mcpServers — check your host configuration", whitelist))
	}

	// Validate every whitelisted server exists in .mcp.json.
	for _, name := range whitelist {
		if _, ok := cfg.McpServers[name]; !ok {
			panic(fmt.Errorf("mcp: server %q is declared in the role's mcp whitelist but not found in .mcp.json — check your host configuration", name))
		}
	}

	// Filter cfg.McpServers to only the whitelisted servers.
	filtered := &Config{McpServers: make(map[string]ServerDef, len(whitelist))}
	for _, name := range whitelist {
		filtered.McpServers[name] = cfg.McpServers[name]
	}

	httpClient := httpx.NewClient(0) // no timeout; transports manage their own
	tools, clients := LoadServers(context.Background(), filtered, log.Printf, httpClient)
	if len(tools) == 0 && len(clients) == 0 {
		return &Bundle{}
	}
	return &Bundle{
		Tools:   tools,
		Clients: clients,
	}
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewBundle).ToSelf()
	return m
}
