// Package orchestrator abstracts "given a session task, run a container
// and hand back its stdio". One real implementation (DockerOrchestrator)
// shells out to the docker CLI; tests use a FakeOrchestrator that runs
// the agent binary directly without any container.
//
// Separating the surface lets the loop package be unit-tested without a
// real docker daemon, and lets us swap to containerd or podman later
// without touching the IO-forwarding code.
package orchestrator

import (
	"context"
	"io"
)

// Task is the parameter bag for Start. It mirrors client.Task minus
// transport-only metadata; the loop builds this from a poll response.
//
// HostAddendumPath / AgentBinaryPath are host-side paths the orchestrator
// bind-mounts into the container. HostWorkdir is where the agent should
// be invoked from inside the container (canonical /workspace per spec).
//
// ContainerID is the long-lived container the platform already knows
// about for this session (empty for a fresh session, or after the
// previous container was reaped). When non-empty the orchestrator tries
// to `docker exec` into it; falling back to a fresh container if the id
// is stale (host rebooted, manual `docker rm`, etc.). The bound image
// stays whatever was used at first create — see docs/agent-config.md
// §"Session 模型".
type Task struct {
	SessionID int64
	Image     string
	// Entrypoint overrides the container's PID 1. First element is
	// the argv0 passed to docker --entrypoint; subsequent elements
	// are appended after the image name as CMD args. Empty / nil
	// falls back to the orchestrator's built-in default
	// (`/usr/bin/sleep infinity`) so the container stays alive as a
	// passive docker-exec sandbox.
	Entrypoint []string
	// Build, when non-nil, tells the orchestrator to materialise
	// Image via `docker build` against a Dockerfile inside the host
	// repo (HostWorkdir/Build.Dockerfile) before `docker create`.
	// The Image tag is the deterministic name the spawner sends
	// down; the orchestrator only builds when `docker image inspect
	// <Image>` reports the tag missing, so re-uses are free.
	Build            *BuildSpec
	AgentBinaryPath  string
	HostAddendumPath string
	HostWorkdir      string
	Env              map[string]string
	ContainerID      string
}

// BuildSpec describes the docker-build inputs for a Task. Paths are
// host-repo-relative; the orchestrator joins them with HostWorkdir to
// reach files on disk.
type BuildSpec struct {
	Dockerfile string
	Context    string
	Args       map[string]string
}

// Handle is the running container's stdio + wait surface. Stdin / Stdout
// behave like os.Stdin / os.Stdout on the container's perspective:
// writing to Stdin sends a line to the agent; reading from Stdout pulls
// the next byte the agent emitted. Stop sends SIGTERM (graceful) and
// returns the exit code via Wait.
//
// ContainerID returns the docker container id this handle is execing
// into. Stable for the lifetime of the handle: the runner persists it
// back to the platform after Start so the next trigger on the same
// session reuses the same container (state preservation across runs).
type Handle interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Wait() (exitCode int, err error)
	Stop(ctx context.Context) error
	ContainerID() string
}

// Orchestrator starts a session and returns its handle. The contract is
// "Start blocks just long enough to launch the container, then returns";
// long-running IO is consumed via the Handle. Start may fail synchronously
// (image pull failure / cgroup denied / missing bind-mount source); the
// loop translates those into terminal sessions with the error message
// surfaced as `error_message`.
//
// RemoveContainer is used by the runner's cleanup sweeper to honour
// platform-flagged cleanups (archive / user-delete / 7-day idle). The
// implementation must be idempotent — calling on an already-gone id is
// not an error, so the cleanup ACK is safe to retry.
type Orchestrator interface {
	Start(ctx context.Context, task Task) (Handle, error)
	RemoveContainer(ctx context.Context, containerID string) error
}
