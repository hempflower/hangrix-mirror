package mcp

import (
	"context"
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

// NewBundle loads .mcp.json from the workspace root and returns the
// connected tools + clients. Individual server failures are logged as
// warnings; the agent continues startup without that server's tools.
func NewBundle(deps *Deps) *Bundle {
	cfg, err := Load("/workspace")
	if err != nil {
		log.Printf("WARN: mcp: %v", err)
		return &Bundle{}
	}
	if cfg == nil {
		return &Bundle{}
	}

	httpClient := httpx.NewClient(0) // no timeout; transports manage their own
	tools, clients := LoadServers(context.Background(), cfg, log.Printf, httpClient)
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
