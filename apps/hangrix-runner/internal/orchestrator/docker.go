// DockerOrchestrator is the production Orchestrator. It shells out to the
// docker CLI rather than the API socket so the runner inherits whatever
// the user has already configured (registry auth, BuildKit, rootless).
//
// Container layout the orchestrator establishes:
//
//	/usr/local/bin/hangrix-agent   ← bind mount of AgentBinaryPath
//	/opt/hangrix/host_addendum.md  ← bind mount of HostAddendumPath (ro)
//	/workspace                     ← bind mount of HostWorkdir (rw)
//
// Env passed via -e, with HANGRIX_HOST_ADDENDUM pointing at the
// in-container path above. HANGRIX_SESSION_TOKEN is sourced from
// Task.Env (the runner has the plaintext from the dispatch response
// and merges it in before calling Start).
//
// Network: defaults to Docker's `bridge` so each agent container is
// network-isolated from the host and from sibling sessions. The agent
// reaches the platform via HANGRIX_PLATFORM_BASE_URL, which must
// resolve from inside that network (use `host.docker.internal` on
// Docker Desktop, the host's routable IP on bare metal, or set
// HANGRIX_RUNNER_DOCKER_NETWORK to drop sessions onto an existing
// user-defined bridge that already contains the server). Operators
// who really want the previous "share the host stack" behaviour can
// still set HANGRIX_RUNNER_DOCKER_NETWORK=host.
package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DockerOrchestrator struct {
	bin string // path to docker CLI
	// pathMap rewrites bind-mount source paths from the runner's view
	// onto the docker daemon's view. Each entry is `from=to`; the
	// first matching prefix wins. Needed when the runner runs inside
	// a container whose workspace bind-mount points elsewhere on the
	// host (devcontainer / docker-from-docker). Populated from the
	// HANGRIX_RUNNER_HOST_PATH_MAP env at construction time.
	pathMap []pathRemap
	// network sets `docker run --network <value>`. Defaults to
	// "bridge" — each session gets Docker's stock isolated bridge,
	// so a misbehaving agent can't reach the host's loopback, the
	// runner's own ports, or sibling sessions. Operators wiring the
	// agent onto an existing user-defined bridge (typical in
	// docker-compose / devcontainer deploys where the server lives
	// on the same network) override via HANGRIX_RUNNER_DOCKER_NETWORK.
	// The legacy "share the host stack" behaviour is opt-in via
	// HANGRIX_RUNNER_DOCKER_NETWORK=host.
	network string
}

type pathRemap struct {
	from string
	to   string
}

func NewDocker(bin string) *DockerOrchestrator {
	if bin == "" {
		bin = "docker"
	}
	network := strings.TrimSpace(os.Getenv("HANGRIX_RUNNER_DOCKER_NETWORK"))
	if network == "" {
		// Isolated by default — never share the host's network stack
		// unless the operator opts in explicitly. See package doc.
		network = "bridge"
	}
	return &DockerOrchestrator{
		bin:     bin,
		pathMap: parsePathMap(os.Getenv("HANGRIX_RUNNER_HOST_PATH_MAP")),
		network: network,
	}
}

// parsePathMap reads "from=to[,from2=to2…]" into a normalized list.
// Each "from" is matched as a directory prefix; "to" replaces it
// verbatim. Trailing slashes are normalised away so `/workspaces` and
// `/workspaces/` behave the same.
func parsePathMap(raw string) []pathRemap {
	if raw == "" {
		return nil
	}
	out := make([]pathRemap, 0, 2)
	for _, pair := range filepath.SplitList(strings.ReplaceAll(raw, ",", string(filepath.ListSeparator))) {
		// SplitList honours the OS path-list separator; the comma
		// fallback above keeps the env value portable across shells
		// that escape `:` differently.
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 || eq == len(pair)-1 {
			continue
		}
		from := strings.TrimRight(pair[:eq], "/")
		to := strings.TrimRight(pair[eq+1:], "/")
		if from != "" && to != "" {
			out = append(out, pathRemap{from: from, to: to})
		}
	}
	return out
}

// remapHostPath applies the pathMap to a bind-mount source. Returns
// the input unchanged when no entry matches.
func (o *DockerOrchestrator) remapHostPath(p string) string {
	for _, m := range o.pathMap {
		if p == m.from {
			return m.to
		}
		if strings.HasPrefix(p, m.from+"/") {
			return m.to + p[len(m.from):]
		}
	}
	return p
}

// dockerHandle wraps an exec.Cmd and its stdio pipes. The exec.Cmd is
// `docker run --rm -i ...`; we drive the agent's stdin through the
// command's stdin pipe and read agent stdout through its stdout pipe.
type dockerHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader
}

func (h *dockerHandle) Stdin() io.WriteCloser { return h.stdin }
func (h *dockerHandle) Stdout() io.Reader     { return h.stdout }
func (h *dockerHandle) Stderr() io.Reader     { return h.stderr }

func (h *dockerHandle) Wait() (int, error) {
	if err := h.cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

func (h *dockerHandle) Stop(ctx context.Context) error {
	if h.cmd.Process == nil {
		return nil
	}
	// docker run --rm propagates SIGTERM into the container. Kill via the
	// child process so the docker CLI client also exits cleanly.
	return h.cmd.Process.Signal(os.Interrupt)
}

// Start assembles the `docker run` args, opens stdio pipes, and starts
// the process. The container entrypoint is forced to /usr/local/bin/
// hangrix-agent so callers can pass any image with `git` + libc.
//
// Pre-flight: each bind-mount source must exist on the host.
// Image-pull failures surface as exit codes from the docker CLI itself.
func (o *DockerOrchestrator) Start(ctx context.Context, t Task) (Handle, error) {
	if t.AgentBinaryPath == "" {
		return nil, fmt.Errorf("AgentBinaryPath is required")
	}
	// Docker's bind-mount creates a directory on the target path when
	// the source is itself a directory. The container then panics with
	// `exec: "/usr/local/bin/hangrix-agent": is a directory` deep
	// inside runc. Catch that here so the error names the real problem
	// (host-side cache holds a dir, not a file) instead.
	info, err := os.Stat(t.AgentBinaryPath)
	if err != nil {
		return nil, fmt.Errorf("agent binary not found at %s: %w", t.AgentBinaryPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("agent binary at %s is not a regular file (mode=%s); the runner cache is corrupted — remove the path and re-enroll", t.AgentBinaryPath, info.Mode())
	}
	if t.Image == "" {
		return nil, fmt.Errorf("Image is required")
	}
	if t.HostWorkdir == "" {
		return nil, fmt.Errorf("HostWorkdir is required")
	}
	if err := os.MkdirAll(t.HostWorkdir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure workdir %s: %w", t.HostWorkdir, err)
	}

	args := []string{
		"run", "--rm", "-i",
		"--network", o.network,
		"--workdir", "/workspace",
		"--entrypoint", "/usr/local/bin/hangrix-agent",
		"-v", o.absMount(t.AgentBinaryPath, "/usr/local/bin/hangrix-agent", true),
		"-v", o.absMount(t.HostWorkdir, "/workspace", false),
	}
	if t.HostAddendumPath != "" {
		args = append(args, "-v", o.absMount(t.HostAddendumPath, "/opt/hangrix/host_addendum.md", true))
	}
	// Surface canonical env keys that point at in-container paths.
	env := map[string]string{}
	for k, v := range t.Env {
		env[k] = v
	}
	if t.HostAddendumPath != "" {
		env["HANGRIX_HOST_ADDENDUM"] = "/opt/hangrix/host_addendum.md"
	}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, t.Image)

	cmd := exec.CommandContext(ctx, o.bin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start docker: %w", err)
	}
	return &dockerHandle{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

// absMount turns (host, container) into the `-v` arg form docker
// expects. Trailing :ro flag added when ro is true. host is converted
// to an absolute path because docker rejects relative bind-mount
// sources, then run through the orchestrator's path map (if any) so a
// devcontainer's view of `/workspaces/foo` becomes the docker
// daemon's `/home/user/foo`.
func (o *DockerOrchestrator) absMount(host, container string, ro bool) string {
	if !filepath.IsAbs(host) {
		if abs, err := filepath.Abs(host); err == nil {
			host = abs
		}
	}
	host = o.remapHostPath(host)
	v := host + ":" + container
	if ro {
		v += ":ro"
	}
	return v
}

