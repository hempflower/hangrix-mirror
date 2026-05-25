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
// Container lifecycle: as of migration 00004 the agent process no
// longer doubles as PID 1. The container is created with `sleep
// infinity` as PID 1 so it survives between agent runs; each run is a
// fresh `docker exec hangrix-agent` into the same container. The
// platform tracks container_id per session; the orchestrator reuses
// when Task.ContainerID is set and the container is still alive on
// the host, else creates a fresh one. State (caches, build artefacts,
// partial edits) survives across triggers on the same session.
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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// dockerHandle wraps the `docker exec -i` child process that runs the
// agent inside a long-lived container. The cmd is the exec child, NOT
// the container itself — Stop closes that exec process; the surrounding
// container is left running so the next trigger on the same session
// reuses it.
type dockerHandle struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.Reader
	stderr      io.Reader
	containerID string
}

func (h *dockerHandle) Stdin() io.WriteCloser { return h.stdin }
func (h *dockerHandle) Stdout() io.Reader     { return h.stdout }
func (h *dockerHandle) Stderr() io.Reader     { return h.stderr }
func (h *dockerHandle) ContainerID() string   { return h.containerID }

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
	// SIGTERM the docker-exec child; the container itself keeps running
	// (PID 1 = sleep infinity). The agent inside sees its stdin/stdout
	// close and exits, mirroring the old --rm behaviour minus the
	// container teardown.
	return h.cmd.Process.Signal(os.Interrupt)
}

// Start runs one agent in the session's long-lived container. The first
// run on a session creates the container (with `sleep infinity` as PID 1)
// and reports the freshly-minted id to the caller via Handle.ContainerID;
// subsequent runs reuse the id passed in via Task.ContainerID. The image
// is bound at create-time — if it later changes in role config we keep
// using the old image (per docs/agent-config.md §"Session 模型") so the
// container state stays usable across triggers; the user can force a
// rebuild by deleting the session.
//
// Pre-flight: each bind-mount source must exist on the host. Image-pull
// failures surface as exit codes from the docker CLI itself.
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

	if err := o.ensureImage(ctx, t); err != nil {
		return nil, err
	}

	containerID, err := o.resolveContainer(ctx, t)
	if err != nil {
		return nil, err
	}

	// `docker exec -i` opens stdin and pipes the agent's stdout/stderr
	// back to us — same shape the runner used under `docker run -i`.
	// --workdir is set at exec time so a future `cd` inside the
	// container doesn't bleed across runs.
	execArgs := []string{"exec", "-i", "--workdir", "/workspace"}
	env := map[string]string{}
	for k, v := range t.Env {
		env[k] = v
	}
	if t.HostAddendumPath != "" {
		env["HANGRIX_HOST_ADDENDUM"] = "/opt/hangrix/host_addendum.md"
	}
	for k, v := range env {
		execArgs = append(execArgs, "-e", k+"="+v)
	}
	execArgs = append(execArgs, containerID, "/usr/local/bin/hangrix-agent")

	cmd := exec.CommandContext(ctx, o.bin, execArgs...)
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
		return nil, fmt.Errorf("start docker exec: %w", err)
	}
	return &dockerHandle{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, containerID: containerID}, nil
}

// resolveContainer returns the container id the agent should exec into:
// reuse Task.ContainerID when the container still exists, else create a
// fresh one. A reused container is `docker start`ed unconditionally —
// the call is idempotent on a running container and brings up one that
// was stopped (e.g. host reboot with `--restart=no`).
func (o *DockerOrchestrator) resolveContainer(ctx context.Context, t Task) (string, error) {
	if t.ContainerID != "" && o.containerExists(ctx, t.ContainerID) {
		if err := o.run(ctx, "start", t.ContainerID); err != nil {
			return "", fmt.Errorf("start existing container %s: %w", t.ContainerID, err)
		}
		return t.ContainerID, nil
	}
	return o.createContainer(ctx, t)
}

// containerExists is a fast `docker inspect` probe — used to detect a
// stale Task.ContainerID (host rebooted with no restart policy, operator
// `docker rm`d manually, runner re-enrolled on a fresh host). On any
// error we fall through to creating a fresh container; the platform's
// SetSessionContainer call will rewrite the id.
func (o *DockerOrchestrator) containerExists(ctx context.Context, id string) bool {
	cmd := exec.CommandContext(ctx, o.bin, "inspect", "--type=container", "--format={{.Id}}", id)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// createContainer materialises a fresh long-lived container for the
// session: PID 1 = `sleep infinity`, all bind mounts set up at create
// time (they can't be added at exec time). Returns the full container
// id docker echoed back on stdout. The container is left in the running
// state so the immediate exec succeeds.
func (o *DockerOrchestrator) createContainer(ctx context.Context, t Task) (string, error) {
	entrypoint, cmdArgs := dockerEntrypoint(t.Entrypoint)
	args := []string{
		"create",
		"--network", o.network,
		"--entrypoint", entrypoint,
		"-v", o.absMount(t.AgentBinaryPath, "/usr/local/bin/hangrix-agent", true),
		"-v", o.absMount(t.HostWorkdir, "/workspace", false),
	}
	if t.HostAddendumPath != "" {
		args = append(args, "-v", o.absMount(t.HostAddendumPath, "/opt/hangrix/host_addendum.md", true))
	}
	for _, vol := range t.Volumes {
		args = append(args, "-v", vol.Name+":"+vol.Mount)
	}
	args = append(args, t.Image)
	args = append(args, cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, o.bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker create: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	id := strings.TrimSpace(stdout.String())
	if id == "" {
		return "", fmt.Errorf("docker create: empty container id (stderr: %s)", strings.TrimSpace(stderr.String()))
	}
	if err := o.run(ctx, "start", id); err != nil {
		// Best-effort cleanup so we don't leak a created-but-never-
		// started container. Ignored on error — the platform's reaper
		// will sweep up the leak eventually if it sticks around.
		_ = o.run(context.Background(), "rm", "-f", id)
		return "", fmt.Errorf("docker start %s: %w", id, err)
	}
	return id, nil
}

// ensureImage materialises Task.Image when Task.Build is set. The
// no-Build path is a no-op: the regular `docker create` further down
// will pull (or surface a missing-image error) on its own.
//
// With Build set we cache by tag: a `docker image inspect <tag>` probe
// short-circuits when the image is already present locally. The tag
// itself is computed by the spawner — deterministic on (repo id +
// Dockerfile path + build args), so two sessions with the same build
// spec hit the cache, and a spec change yields a fresh tag.
//
// On cache miss we shell out to `docker build` with BuildKit enabled
// (the project's Dockerfiles use `# syntax=docker/dockerfile:1.7`
// heredocs which legacy build can't parse). The Dockerfile path and
// the build context are resolved against HostWorkdir — the cloned
// repo on disk — and then run through the path map so the docker
// daemon sees the host's view, not the runner-container's view.
//
// Build stderr is captured and surfaced verbatim; build failures
// turn into a SessionStatusFailed with the docker error preserved so
// the operator can fix their Dockerfile without digging through
// runner logs.
func (o *DockerOrchestrator) ensureImage(ctx context.Context, t Task) error {
	if t.Build == nil {
		return nil
	}
	if t.Build.Dockerfile == "" {
		return fmt.Errorf("build.dockerfile is required")
	}
	if t.Image == "" {
		return fmt.Errorf("build set but Image (the target tag) is empty")
	}

	// Cache hit?
	probe := exec.CommandContext(ctx, o.bin, "image", "inspect", "--format={{.Id}}", t.Image)
	probe.Stdout = io.Discard
	probe.Stderr = io.Discard
	if probe.Run() == nil {
		return nil
	}

	dockerfilePath := filepath.Join(t.HostWorkdir, t.Build.Dockerfile)
	if _, err := os.Stat(dockerfilePath); err != nil {
		return fmt.Errorf("dockerfile not found at %s: %w", dockerfilePath, err)
	}
	contextDir := t.HostWorkdir
	if t.Build.Context != "" && t.Build.Context != "." {
		contextDir = filepath.Join(t.HostWorkdir, t.Build.Context)
	}
	if _, err := os.Stat(contextDir); err != nil {
		return fmt.Errorf("build context not found at %s: %w", contextDir, err)
	}

	args := []string{
		"build",
		"-t", t.Image,
		"-f", o.remapHostPath(dockerfilePath),
	}
	// Sorted args keeps the docker invocation stable for the same
	// spec — easier to diff in logs when a build flakes.
	keys := make([]string, 0, len(t.Build.Args))
	for k := range t.Build.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+t.Build.Args[k])
	}
	args = append(args, o.remapHostPath(contextDir))

	cmd := exec.CommandContext(ctx, o.bin, args...)
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build %s: %w (stderr: %s)", t.Image, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// dockerEntrypoint folds host-supplied Task.Entrypoint into the pair
// docker create needs: a string for --entrypoint (the argv0) and a
// slice of CMD args appended after the image name. Empty / nil input
// returns the orchestrator's built-in default — `/usr/bin/sleep
// infinity` — which keeps the container alive as a passive docker-
// exec sandbox.
func dockerEntrypoint(spec []string) (string, []string) {
	if len(spec) == 0 {
		return "/usr/bin/sleep", []string{"infinity"}
	}
	return spec[0], append([]string(nil), spec[1:]...)
}

// run is the no-output docker invocation helper used by start / rm.
// stderr is captured so error messages carry the docker complaint.
func (o *DockerOrchestrator) run(ctx context.Context, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, o.bin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// WorkflowContainer creates a long-lived container for workflow job
// execution. Like the agent session path it uses `sleep infinity` as PID 1,
// but it does NOT bind-mount the agent binary or host addendum — the
// container is purely a sandbox for `docker exec bash -lc <step>`.
// When build is non-nil the image is materialised via docker build before
// the container is created.
func (o *DockerOrchestrator) WorkflowContainer(ctx context.Context, image string, build *BuildSpec, entrypoint []string, hostWorkdir string, env map[string]string, volumes []Volume) (string, error) {
	if image == "" {
		return "", fmt.Errorf("image is required")
	}
	if hostWorkdir == "" {
		return "", fmt.Errorf("hostWorkdir is required")
	}
	if err := os.MkdirAll(hostWorkdir, 0o755); err != nil {
		return "", fmt.Errorf("ensure workdir %s: %w", hostWorkdir, err)
	}

	// Resolve the image (pull or build) before creating the container.
	if err := o.ensureImage(ctx, Task{
		Image:       image,
		Build:       build,
		HostWorkdir: hostWorkdir,
	}); err != nil {
		return "", fmt.Errorf("ensure image: %w", err)
	}

	ent, cmdArgs := dockerEntrypoint(entrypoint)
	args := []string{
		"create",
		"--network", o.network,
		"--entrypoint", ent,
		"-v", o.absMount(hostWorkdir, "/workspace", false),
	}
	for _, vol := range volumes {
		args = append(args, "-v", vol.Name+":"+vol.Mount)
	}
	// Workflow runtime env vars (HANGRIX_WORKFLOW_*) are injected at exec
	// time via Exec, not at container create time — they vary per run.
	args = append(args, image)
	args = append(args, cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, o.bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker create (workflow): %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	id := strings.TrimSpace(stdout.String())
	if id == "" {
		return "", fmt.Errorf("docker create (workflow): empty container id (stderr: %s)", strings.TrimSpace(stderr.String()))
	}
	if err := o.run(ctx, "start", id); err != nil {
		_ = o.run(context.Background(), "rm", "-f", id)
		return "", fmt.Errorf("docker start %s: %w", id, err)
	}
	return id, nil
}

// Exec runs a command inside an existing container. The returned
// ExecHandle streams stdout/stderr; the caller drains them and calls
// Wait to collect the exit code. The command is run via `docker exec -i`
// with the given workdir and env vars.
func (o *DockerOrchestrator) Exec(ctx context.Context, containerID, workdir string, env map[string]string, args ...string) (ExecHandle, error) {
	if containerID == "" {
		return nil, fmt.Errorf("containerID is required")
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("args is required")
	}
	execArgs := []string{"exec", "-i"}
	if workdir != "" {
		execArgs = append(execArgs, "--workdir", workdir)
	}
	for k, v := range env {
		execArgs = append(execArgs, "-e", k+"="+v)
	}
	execArgs = append(execArgs, containerID)
	execArgs = append(execArgs, args...)

	cmd := exec.CommandContext(ctx, o.bin, execArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start docker exec: %w", err)
	}
	return &execHandle{cmd: cmd, stdout: stdout, stderr: stderr}, nil
}

// execHandle is the concrete ExecHandle returned by DockerOrchestrator.Exec.
type execHandle struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (h *execHandle) Stdout() io.ReadCloser { return h.stdout }
func (h *execHandle) Stderr() io.ReadCloser { return h.stderr }
func (h *execHandle) Wait() (int, error) {
	if err := h.cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// RemoveContainer force-removes a container by id. Called by the runner's
// cleanup sweeper when the platform flags a container for removal
// (archive, user-delete, 7-day idle). Returns nil when the container is
// already gone so the cleanup ACK is idempotent.
func (o *DockerOrchestrator) RemoveContainer(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	if !o.containerExists(ctx, id) {
		return nil
	}
	return o.run(ctx, "rm", "-f", id)
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
