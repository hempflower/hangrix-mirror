// Package app is the agent's top-level entry point as resolved from the
// ioc container. main.go does:
//
//	c.Load(... all modules ...)
//	ioc.Get[*app.App](c).Run(ctx)
//
// so this package owns the lifecycle (start banner, run the loop, stop
// banner, error fan-out) while delegating every component-level concern
// to the modules wired below it.
package app

import (
	"context"
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/mcp"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/prompt"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/runtime"
)

type Deps struct {
	Loop      *runtime.Loop
	Writer    *ipc.Writer
	Assembled *prompt.Assembled
	MCPBundle *mcp.Bundle
}

type App struct {
	loop      *runtime.Loop
	writer    *ipc.Writer
	assembled *prompt.Assembled
	mcpBundle *mcp.Bundle
}

func New(deps *Deps) *App {
	return &App{
		loop:      deps.Loop,
		writer:    deps.Writer,
		assembled: deps.Assembled,
		mcpBundle: deps.MCPBundle,
	}
}

// Run executes the agent's main loop until ctx is cancelled or stdin
// closes. Init errors are not Run's concern — they surface as panics
// during container construction and are caught by main.go's recover.
func (a *App) Run(ctx context.Context) error {
	defer a.mcpBundle.Close()

	_ = a.writer.Log("info", fmt.Sprintf("agent starting; system prompt layers: %v", a.assembled.KeptLayers))
	if err := a.loop.Run(ctx); err != nil {
		_ = a.writer.Log("error", err.Error())
		return err
	}
	_ = a.writer.Log("info", "agent stopping")
	return nil
}
