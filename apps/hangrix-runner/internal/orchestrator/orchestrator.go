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
type Task struct {
	SessionID        int64
	Image            string
	AgentBinaryPath  string
	HostAddendumPath string
	HostWorkdir      string
	Env              map[string]string
}

// Handle is the running container's stdio + wait surface. Stdin / Stdout
// behave like os.Stdin / os.Stdout on the container's perspective:
// writing to Stdin sends a line to the agent; reading from Stdout pulls
// the next byte the agent emitted. Stop sends SIGTERM (graceful) and
// returns the exit code via Wait.
type Handle interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Wait() (exitCode int, err error)
	Stop(ctx context.Context) error
}

// Orchestrator starts a session and returns its handle. The contract is
// "Start blocks just long enough to launch the container, then returns";
// long-running IO is consumed via the Handle. Start may fail synchronously
// (image pull failure / cgroup denied / missing bind-mount source); the
// loop translates those into terminal sessions with the error message
// surfaced as `error_message`.
type Orchestrator interface {
	Start(ctx context.Context, task Task) (Handle, error)
}
