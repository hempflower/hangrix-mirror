// Package config parses the runner's CLI flags + env. The runner has two
// subcommands and intentionally few flags:
//
//	hangrix-runner enroll --server URL --token hgxe_...
//	hangrix-runner serve  [--state-dir DIR]
//
// Everything the runner needs at run-time (server URL, in-container LLM
// endpoint, default image, agent binary path, sha) is fetched from the
// platform during enroll and persisted under --state-dir (default
// ~/.hangrix). The `serve` subcommand reads that state and refreshes the
// bootstrap on every startup, so an operator who changes a config field
// server-side doesn't need to touch the runner.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	// StateDir is where the runner persists state.json and the cached
	// agent binary. Both subcommands accept --state-dir; defaults to
	// $HOME/.hangrix.
	StateDir string

	// enroll-only:
	Server      string
	EnrollToken string

	// serve-only: a host docker binary override for non-default install
	// layouts. Most operators leave this unset.
	DockerBin string
}

func Parse(args []string) (sub string, cfg *Config, err error) {
	if len(args) < 1 {
		return "", nil, fmt.Errorf("usage: hangrix-runner <enroll|serve> [flags]")
	}
	sub = args[0]
	cfg = &Config{
		StateDir:  envOr("HANGRIX_RUNNER_STATE_DIR", defaultHangrixRoot()),
		Server:    envOr("HANGRIX_RUNNER_SERVER", ""),
		DockerBin: envOr("HANGRIX_RUNNER_DOCKER_BIN", "docker"),
	}
	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "persistent state directory")
	switch sub {
	case "enroll":
		fs.StringVar(&cfg.Server, "server", cfg.Server, "Hangrix server base URL")
		fs.StringVar(&cfg.EnrollToken, "token", cfg.EnrollToken, "enrollment token (hgxe_...)")
	case "serve":
		fs.StringVar(&cfg.DockerBin, "docker", cfg.DockerBin, "docker CLI binary")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return sub, cfg, err
	}
	if sub == "enroll" {
		if cfg.Server == "" {
			return sub, cfg, fmt.Errorf("--server is required for enroll")
		}
		if cfg.EnrollToken == "" {
			return sub, cfg, fmt.Errorf("--token is required for enroll")
		}
		// Strip trailing slashes so url concatenation is predictable.
		for len(cfg.Server) > 0 && cfg.Server[len(cfg.Server)-1] == '/' {
			cfg.Server = cfg.Server[:len(cfg.Server)-1]
		}
	}
	return sub, cfg, nil
}

// defaultHangrixRoot is where the runner persists its state in the
// absence of any override. We match the kubectl / gh idiom: a dotfile
// directory in the user's home. Operators running the runner as a system
// service should set HANGRIX_RUNNER_STATE_DIR explicitly (e.g.
// /var/lib/hangrix) — we don't try to detect that case here.
func defaultHangrixRoot() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".hangrix")
	}
	return "./.hangrix"
}

func envOr(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}
