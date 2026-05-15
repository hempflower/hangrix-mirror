// Command hangrix-agent is the long-running per-role agent process.
// It is bind-mounted into the runner's container by M6c, started with a
// curated env, and communicates with its runner over stdin/stdout
// JSON-Lines.
//
// The process is assembled by an ioc container (mirroring the host
// hangrix app): each component module — config, llm, mcp, tools,
// prompt, ipc, runtime, app — registers its providers in-package, the
// container resolves the dependency graph, and main resolves the root
// *app.App and runs it. Construction errors (missing env, bad tool
// catalogue, unreadable agent bundle) panic from inside the relevant
// provider; main.go's deferred recover converts those panics into the
// same single stderr line the runner used to see, so the operator-
// visible failure shape is unchanged from the pre-ioc layout.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/app"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/mcp"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/prompt"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/runtime"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// buildContainer registers every module the agent needs and returns the
// ready-to-resolve container. Split out from main so wiring_test can
// exercise the dependency graph without spawning the runtime loop.
func buildContainer() *ioc.Container {
	c := ioc.NewContainer()
	c.Provide(func() *ioc.Container { return c }).ToSelf()
	c.Load(
		config.Module(),
		llm.Module(),
		mcp.Module(),
		tools.Module(),
		prompt.Module(),
		ipc.Module(),
		runtime.Module(),
		app.Module(),
	)
	return c
}

func main() {
	// Provider panics carry the original init error; recover here to
	// translate them back into the documented "one stderr line + exit 1"
	// shape. Without this, the runner would see a verbose Go panic
	// stack and have to parse the first line manually.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "hangrix-agent:", r)
			os.Exit(1)
		}
	}()

	// SIGTERM / SIGINT → graceful: cancel the root context, the runtime
	// loop returns from its blocked stdin read once the shutdown
	// frame propagates (or the runner closes the pipe).
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	a := ioc.Get[*app.App](buildContainer())
	if err := a.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "hangrix-agent:", err)
		os.Exit(1)
	}
}
