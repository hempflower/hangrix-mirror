package mcp

import (
	"context"
	"log"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	"github.com/hangrix/hangrix/pkg/ioc"
	mcpclient "github.com/mark3labs/mcp-go/client"
)

// Bundle pairs the MCP tools with the client slice (must be closed on shutdown).
type Bundle struct {
	Tools   []local.Tool
	Clients []*mcpclient.Client
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
	if err := cfg.Validate(); err != nil {
		log.Printf("WARN: mcp: %v", err)
		return &Bundle{}
	}

	tools, clients := LoadServers(context.Background(), cfg, log.Printf)
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
