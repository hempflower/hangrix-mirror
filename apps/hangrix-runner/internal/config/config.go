// Package config parses the runner's CLI flags + env. The runner has
// three subcommands and intentionally few flags:
//
//	hangrix-runner enroll --server URL --token hgxe_...
//	hangrix-runner serve  [--state-dir DIR] [--auto-update] [--parallelism N]
//	hangrix-runner update [--state-dir DIR] [--force]
//
// Everything the runner needs at run-time (server URL, in-container LLM
// endpoint, default image, agent binary path, sha) is fetched from the
// platform during enroll and persisted under --state-dir (default
// ~/.hangrix). The `serve` subcommand reads that state and refreshes the
// bootstrap on every startup, so an operator who changes a config field
// server-side doesn't need to touch the runner. `update` uses the same
// state to compare the server's embedded `hangrix-runner_<goos>_<goarch>`
// artefact against the binary on disk and self-replace when they drift.
// `serve --auto-update` (or HANGRIX_RUNNER_AUTO_UPDATE=1) folds that
// check into the startup path AND re-runs it once a minute while
// serving: if a new build is available, replace the binary on disk and
// exit 0 so the supervisor restarts onto it.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

	// serve-only: when true, the runner checks the server's embedded
	// build at startup and again every minute while serving, and self-
	// replaces if it differs from the binary on disk. On a successful
	// replace, serve exits cleanly (rc=0) so the operator's supervisor
	// restarts onto the new bytes. Defaults off: an opt-in flag keeps a
	// broken upstream build from bricking every runner the moment they
	// next restart.
	AutoUpdate bool

	// serve-only: maximum number of concurrent sessions this runner is
	// willing to drive. Defaults to 16 — enough headroom that a single
	// runner doesn't bottleneck multi-role parallel work in an issue
	// without operator tuning. Each unit of parallelism runs an
	// independent task poller + session driver; the DB claim is
	// FOR UPDATE SKIP LOCKED so the workers never race for the same
	// row. Operators with constrained hosts should lower this — every
	// in-flight session keeps a docker container + repo clone resident.
	Parallelism int

	// update-only: redownload + reinstall even when the on-disk binary
	// already matches the server's advertised SHA. Useful for recovering
	// from a corrupted local binary or rolling out a build that reuses a
	// previous SHA after an aborted swap.
	Force bool

	// serve-only: when true, agent sessions are executed as direct
	// subprocesses (LocalOrchestrator) instead of Docker containers.
	// All /api/runner/* protocol endpoints are still used (real enroll,
	// real heartbeat, real task polling, real message shipping); only
	// session-container orchestration is replaced. Workflow jobs are
	// unaffected and continue using Docker. Defaults off — production
	// runners always use Docker for everything.
	Mock bool
}

func Parse(args []string) (sub string, cfg *Config, err error) {
	if len(args) < 1 {
		return "", nil, fmt.Errorf("usage: hangrix-runner <enroll|serve> [flags]")
	}
	sub = args[0]
	cfg = &Config{
		StateDir:    envOr("HANGRIX_RUNNER_STATE_DIR", defaultHangrixRoot()),
		Server:      envOr("HANGRIX_RUNNER_SERVER", ""),
		DockerBin:   envOr("HANGRIX_RUNNER_DOCKER_BIN", "docker"),
		AutoUpdate:  envTruthy("HANGRIX_RUNNER_AUTO_UPDATE"),
		Parallelism: envInt("HANGRIX_RUNNER_PARALLELISM", 16),
		Mock:        envTruthy("HANGRIX_RUNNER_MOCK"),
	}
	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "persistent state directory")
	switch sub {
	case "enroll":
		fs.StringVar(&cfg.Server, "server", cfg.Server, "Hangrix server base URL")
		fs.StringVar(&cfg.EnrollToken, "token", cfg.EnrollToken, "enrollment token (hgxe_...)")
	case "serve":
		fs.StringVar(&cfg.DockerBin, "docker", cfg.DockerBin, "docker CLI binary")
		fs.BoolVar(&cfg.AutoUpdate, "auto-update", cfg.AutoUpdate, "self-update + exit on startup and every minute while serving when a new binary is available")
		fs.IntVar(&cfg.Parallelism, "parallelism", cfg.Parallelism, "max concurrent sessions to drive (default 16)")
		fs.BoolVar(&cfg.Mock, "mock", cfg.Mock, "run agent as direct subprocess without Docker (mock/local mode)")
	case "update":
		fs.BoolVar(&cfg.Force, "force", false, "redownload even when local SHA matches")
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

// envTruthy reads a boolean-ish env var. Unset / empty / explicit false-
// like values return false; anything we recognise as truthy ("1", "true",
// "yes", "on", case-insensitive) returns true. Unknown strings fall back
// to false so a typo doesn't silently enable a behaviour gate.
func envTruthy(k string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// envInt reads an integer-valued env var; unset / empty / unparseable
// values fall back to def. Non-positive values also fall back so an
// operator can't accidentally configure "0 workers" via env.
func envInt(k string, def int) int {
	raw := strings.TrimSpace(os.Getenv(k))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
