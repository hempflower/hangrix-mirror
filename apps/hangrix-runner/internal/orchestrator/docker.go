// DockerOrchestrator is the production Orchestrator. It shells out to the
// docker CLI rather than the API socket so the runner inherits whatever
// the user has already configured (registry auth, BuildKit, rootless).
//
// Container layout the orchestrator establishes:
//
//	/usr/local/bin/hangrix-agent   ← bind mount of AgentBinaryPath
//	/opt/hangrix/bundle            ← bind mount of HostBundleDir (read-only)
//	/opt/hangrix/host_addendum.md  ← bind mount of HostAddendumPath (ro)
//	/workspace                     ← bind mount of HostWorkdir (rw)
//
// Env passed via -e, with HANGRIX_AGENT_BUNDLE / HANGRIX_HOST_ADDENDUM
// pointing at the in-container paths above. HANGRIX_SESSION_TOKEN is
// sourced from Task.Env (the runner has the plaintext from the dispatch
// response and merges it in before calling Start).
//
// Network: --network host gives the agent direct access to the platform
// LLM proxy + MCP server at the same host:port the runner already uses.
// Inside docker-in-docker setups operators may prefer
// host.docker.internal:8080 via env override.
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
	// network sets `docker run --network <value>`. Defaults to "host"
	// (matches the runner-protocol spec: agent shares the host's
	// network so it can reach localhost-bound platform endpoints).
	// Operators running the runner inside a docker-compose / dev
	// container override this via HANGRIX_RUNNER_DOCKER_NETWORK to
	// drop the agent onto the same bridge network as the server.
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
		network = "host"
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
	if _, err := os.Stat(t.AgentBinaryPath); err != nil {
		return nil, fmt.Errorf("agent binary not found at %s: %w", t.AgentBinaryPath, err)
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
	if t.HostBundleDir != "" {
		args = append(args, "-v", o.absMount(t.HostBundleDir, "/opt/hangrix/bundle", true))
	}
	if t.HostAddendumPath != "" {
		args = append(args, "-v", o.absMount(t.HostAddendumPath, "/opt/hangrix/host_addendum.md", true))
	}
	// Surface canonical env keys that point at in-container paths.
	env := map[string]string{}
	for k, v := range t.Env {
		env[k] = v
	}
	if t.HostBundleDir != "" {
		env["HANGRIX_AGENT_BUNDLE"] = "/opt/hangrix/bundle"
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

