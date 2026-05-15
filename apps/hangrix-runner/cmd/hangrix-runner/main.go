// hangrix-runner is the standalone binary deployed on every machine that
// hosts Hangrix agent containers. It speaks only HTTP outbound — no
// listening sockets — to the platform's /api/runner/* surface.
//
// Subcommands:
//
//	hangrix-runner enroll --server URL --token hgxe_...
//	hangrix-runner serve  [--state-dir DIR] [--docker BIN]
//
// `enroll` redeems an enrollment plaintext, downloads the platform-
// hosted agent binary, and snapshots endpoints / image defaults into
// ~/.hangrix/state.json (override with --state-dir or
// HANGRIX_RUNNER_STATE_DIR). `serve` reads that file, refreshes the
// bootstrap, and starts the heartbeat + task-poll loop. No other
// flags are needed.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/cli"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/config"
)

func main() {
	sub, cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	switch sub {
	case "enroll":
		if err := cli.Enroll(ctx, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "enroll:", err)
			os.Exit(1)
		}
	case "serve":
		if err := cli.Serve(ctx, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  hangrix-runner enroll --server URL --token hgxe_...
  hangrix-runner serve  [--state-dir DIR] [--docker BIN]

flags:
  --state-dir DIR   persistent state (default ~/.hangrix)
  --docker BIN      docker CLI path (default 'docker')

env:
  HANGRIX_RUNNER_STATE_DIR    overrides --state-dir
  HANGRIX_RUNNER_SERVER       overrides --server (enroll only)
  HANGRIX_RUNNER_DOCKER_BIN   overrides --docker`)
}
