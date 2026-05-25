// Package client wraps the runner-facing HTTP surface of the Hangrix
// server. One Client per process; methods are safe for concurrent use
// because the underlying http.Client is.
//
// All routes except Enroll require the Bearer agent token set via
// WithAgentToken (or supplied to New). Enroll trades a one-shot enroll
// token for the long-lived agent token.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base       string
	agentToken string
	http       *http.Client
}

// New makes a Client without an agent token (used for enrollment); set
// the token afterwards with WithAgentToken once you have one.
func New(base string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) WithAgentToken(tok string) *Client {
	c.agentToken = tok
	return c
}

// ---- enroll ----

type EnrollRequest struct {
	EnrollToken  string          `json:"enroll_token"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

type EnrollResponse struct {
	RunnerID   int64            `json:"runner_id"`
	RunnerName string           `json:"runner_name"`
	AgentToken string           `json:"agent_token"`
	Bootstrap  BootstrapPayload `json:"bootstrap"`
}

// BootstrapPayload is the side of the enroll/bootstrap responses that
// tells the runner everything it needs to run with no extra flags:
// endpoints to inject into the agent, the embedded runner-binary
// catalogue (for future self-update), and the cadence parameters
// server and runner must agree on.
type BootstrapPayload struct {
	// Binaries is the catalogue of `hangrix-runner` artefacts embedded
	// in the server build, keyed by AssetName
	// (`hangrix-runner_<goos>_<goarch>`). The runner does not
	// currently auto-download any of these — the agent ships inside
	// the runner — but the field is kept so a self-update path can
	// land in a future commit without a wire-shape change.
	Binaries          map[string]BinaryInfo `json:"binaries"`
	BaseURL           string                `json:"base_url"`
	DefaultAgentImage string                `json:"default_agent_image,omitempty"`
	PollWaitSec       int                   `json:"poll_wait_sec"`
	HeartbeatSec      int                   `json:"heartbeat_sec"`
}

// BinaryInfo is one entry in BootstrapPayload.Binaries. Mirrors the
// server-side handler.binaryInfo. URL is server-relative; the runner
// prepends the same base URL it uses for every other call.
type BinaryInfo struct {
	URL    string `json:"url"`
	Name   string `json:"name"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResponse, error) {
	var out EnrollResponse
	if err := c.do(ctx, http.MethodPost, "/api/runner/enroll", req, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// Bootstrap re-fetches the bootstrap payload using the long-term agent
// token. Called by `serve` at startup so the runner picks up endpoint /
// agent-binary changes the platform made since enroll.
func (c *Client) Bootstrap(ctx context.Context) (*BootstrapPayload, error) {
	var out BootstrapPayload
	if err := c.do(ctx, http.MethodGet, "/api/runner/bootstrap", nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- heartbeat ----

type HeartbeatRequest struct {
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) error {
	return c.do(ctx, http.MethodPost, "/api/runner/heartbeat", req, nil, true)
}

// ---- tasks ----

type Task struct {
	// Kind discriminates the task payload: "agent_session" (default when empty
	// for backward compatibility) or "workflow_job". The runner's worker loop
	// routes to SessionDriver or WorkflowJobDriver accordingly.
	Kind string `json:"kind,omitempty"`

	// ---- workflow_job fields (present when Kind == "workflow_job") ----

	// WorkflowJob is populated when Kind == "workflow_job".
	WorkflowJob *WorkflowJob `json:"workflow_job,omitempty"`

	SessionID int64 `json:"session_id"`
	// HostRepoID is the repository id the session belongs to. The runner
	// uses it to namespace named Docker volumes (e.g. "pnpm-store" becomes
	// "repo-6-pnpm-store") so caches from different repos never collide.
	// Zero means the server hasn't been upgraded yet — the runner passes
	// volume names through verbatim for backward compatibility.
	HostRepoID int64  `json:"host_repo_id,omitempty"`
	AgentImage string `json:"agent_image"`
	// AgentEntrypoint overrides the container's PID 1 (docker
	// --entrypoint plus appended args). Empty / nil means the
	// orchestrator falls back to its built-in default
	// (`/usr/bin/sleep infinity`) so the container is a passive
	// sandbox for docker-exec; set this from host yaml when the
	// image bakes in a supervisor (e.g. s6-overlay /init) that
	// should auto-start background services.
	AgentEntrypoint []string `json:"agent_entrypoint,omitempty"`
	// AgentBuild, when set, tells the orchestrator to materialise
	// AgentImage via `docker build` from a Dockerfile inside the
	// host repo before `docker create`. Empty means AgentImage is
	// a pre-built registry tag the runner pulls.
	AgentBuild *BuildSpec `json:"agent_build,omitempty"`
	Role       string     `json:"role"`
	// Model is the resolved LLM model name the spawner picked for this
	// session (role.llm.model > host.llm.model). Surfaced into the
	// container as HANGRIX_LLM_MODEL so the agent's LLM client knows
	// which model to ask the proxy for.
	Model string `json:"model"`
	// IssueNumber is the per-repo issue this session is bound to. Surfaced
	// into the container as HANGRIX_ISSUE_NUMBER so the agent can construct
	// its contribution-branch ref (issue-<N>/<role>/<slug>). Zero for
	// non-issue sessions (the env var is then left unset).
	IssueNumber   int32             `json:"issue_number,omitempty"`
	WorkingBranch string            `json:"working_branch"`
	BaseBranch    string            `json:"base_branch"`
	HostAddendum  string            `json:"host_addendum"`
	Env           map[string]string `json:"env"`
	SessionToken  string            `json:"session_token"`
	// ContainerID is the long-lived docker container previously created
	// for this session (empty for a fresh session or after a 7-day idle
	// reap). The orchestrator reuses it via `docker exec` when set; it
	// falls back to creating a fresh container if the id is stale.
	ContainerID string `json:"container_id,omitempty"`
	// RepoVariables carries the repo-level variable and secret values
	// (already resolved/decrypted by the server) available for ${VAR_NAME}
	// expansion in the session's Env values. Keys are variable names;
	// values are the plaintext (secrets are decrypted server-side before
	// dispatch).
	//
	// Nil means the server has not been upgraded to support repo variable
	// expansion — the runner treats this as a backward-compat no-op and
	// leaves ${...} references unexpanded.  An empty non-nil map means the
	// server supports expansion but the repo has no variables, so any
	// ${...} reference in task.Env fails the session explicitly.
	RepoVariables map[string]string `json:"repo_variables"`
	// Volumes carries the named volume cache mounts from the host repo's
	// agents.yml container block. The orchestrator adds each as a `-v`
	// bind mount at `docker create` time. Nil/empty means no volumes.
	Volumes []Volume `json:"volumes,omitempty"`
	// McpServers is the role's MCP server whitelist from agents.yml. Each
	// element is a server name that must exist in the workspace .mcp.json.
	// Nil or empty means the role declares no MCP servers — the agent will
	// not load any MCP servers even if .mcp.json is present. The runner
	// injects this as HANGRIX_MCP_SERVERS (comma-separated) into the
	// agent container.
	McpServers []string `json:"mcp_servers,omitempty"`
}

// Volume mirrors agentsconfig.Volume (and server-side volumeDTO) on the
// wire. Name is the Docker volume name; Mount is the in-container path.
type Volume struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
}

// BuildSpec mirrors agentsconfig.Build on the wire. Paths are
// repo-relative; the orchestrator resolves them against HostWorkdir
// (the cloned host-repo checkout) at build time.
type BuildSpec struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// ---- workflow job types ----

// WorkflowJob is the task payload when Task.Kind == "workflow_job".
// It describes a single job to execute inside a one-time container.
type WorkflowJob struct {
	JobRunID       int64             `json:"job_run_id"`
	WorkflowRunID  int64             `json:"workflow_run_id"`
	RepoID         int64             `json:"repo_id"`
	Owner          string            `json:"owner"`
	Name           string            `json:"name"`
	WorkflowName   string            `json:"workflow_name"`
	JobKey         string            `json:"job_key"`
	CheckoutRef    string            `json:"checkout_ref"`
	CommitSHA      string            `json:"commit_sha"`
	Tag            string            `json:"tag,omitempty"`
	EventName      string            `json:"event_name,omitempty"`
	EventCauseID   string            `json:"event_cause_id,omitempty"`
	Container      WorkflowContainer `json:"container"`
	WorkingDir     string            `json:"working_directory"`
	Steps          []WorkflowStep    `json:"steps"`
	TimeoutMinutes int               `json:"timeout_minutes"`
	RepoVariables  map[string]string `json:"repo_variables"`
	// Inputs carries the resolved workflow.dispatch inputs, already
	// transformed to WORKFLOW_INPUT_<UPPER_SNAKE> keys by the server.
	// Nil/empty for non-dispatch events.
	Inputs map[string]string `json:"inputs,omitempty"`
	// WorkflowToken is a short-term hangrix_wf_ token scoped to the workflow
	// run's repo. Workflow steps use it to authenticate against repo-scoped
	// write endpoints (e.g. releases) via Bearer auth.
	WorkflowToken string `json:"workflow_token,omitempty"`
}

// WorkflowContainer carries the resolved container definition for a
// workflow job — pulled from .hangrix/agents.yml and merged with
// workflow/job-level env.
type WorkflowContainer struct {
	Image      string            `json:"image"`
	Build      *BuildSpec        `json:"build,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Env        map[string]string `json:"env"`
	Volumes    []Volume          `json:"volumes"`
}

// WorkflowStep is one shell command in a workflow job.
type WorkflowStep struct {
	Name string `json:"name,omitempty"`
	Run  string `json:"run"`
}

// PollTasks returns (task, true, nil) on a real assignment, (nil, false, nil)
// when the server returned 204 (no work), or (nil, false, err) on transport /
// 5xx. Callers loop on `false` after a small backoff.
func (c *Client) PollTasks(ctx context.Context) (*Task, bool, error) {
	body, status, err := c.raw(ctx, http.MethodGet, "/api/runner/tasks", nil, true)
	if err != nil {
		return nil, false, err
	}
	switch status {
	case http.StatusNoContent:
		return nil, false, nil
	case http.StatusOK:
		var t Task
		if err := json.Unmarshal(body, &t); err != nil {
			return nil, false, fmt.Errorf("decode task: %w", err)
		}
		return &t, true, nil
	default:
		return nil, false, fmt.Errorf("poll tasks: %d %s", status, snippet(body))
	}
}

// ---- session lifecycle ----

func (c *Client) MarkRunning(ctx context.Context, sessionID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runner/sessions/%d/running", sessionID), nil, nil, true)
}

// SetContainer records the long-lived container id the orchestrator
// created (or attached to) for this session. Called once per agent run,
// right after orchestrator.Start succeeds. Idempotent: posting the same
// id again just re-stamps container_last_used_at on the server side,
// which drives the 7-day idle reaper.
func (c *Client) SetContainer(ctx context.Context, sessionID int64, containerID string) error {
	return c.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/runner/sessions/%d/container", sessionID),
		setContainerRequest{ContainerID: containerID}, nil, true)
}

// Ping bumps container_last_used_at so that roster_list last_activity_at
// reflects real-time liveness. Called on every agent interaction
// (tool call, thinking, output).
func (c *Client) Ping(ctx context.Context, sessionID int64) error {
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/runner/sessions/%d/ping", sessionID),
		nil, nil, true)
}

type setContainerRequest struct {
	ContainerID string `json:"container_id"`
}

// ---- container cleanup ----

// CleanupTask is one (session, container) pair the platform wants the
// runner to `docker rm`. Returned by ListCleanupTasks.
type CleanupTask struct {
	SessionID   int64  `json:"session_id"`
	ContainerID string `json:"container_id"`
}

type CleanupTasksResponse struct {
	Tasks []CleanupTask `json:"tasks"`
}

// ListCleanupTasks polls the platform for containers this runner should
// remove. The endpoint is keyed off the agent token (so the platform
// knows which runner is asking) and returns at most ~50 entries per
// call; the runner's sweeper loops until it gets an empty page.
func (c *Client) ListCleanupTasks(ctx context.Context) (*CleanupTasksResponse, error) {
	var out CleanupTasksResponse
	if err := c.do(ctx, http.MethodGet, "/api/runner/cleanup-tasks", nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// MarkCleanupDone reports that `docker rm` of the session's container
// succeeded (or that the container was already gone — see
// orchestrator.DockerOrchestrator.RemoveContainer for the no-op path).
// The platform clears container_id + container_cleanup_pending in one
// UPDATE on receipt.
func (c *Client) MarkCleanupDone(ctx context.Context, sessionID int64) error {
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/runner/cleanup-tasks/%d/done", sessionID), nil, nil, true)
}

type TerminateRequest struct {
	Status   string `json:"status"`
	ExitCode *int32 `json:"exit_code,omitempty"`
	Message  string `json:"message,omitempty"`
}

func (c *Client) Terminate(ctx context.Context, sessionID int64, req TerminateRequest) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runner/sessions/%d/terminate", sessionID), req, nil, true)
}

// ---- workflow job callbacks ----

// MarkWorkflowJobRunning signals the platform that the runner has claimed
// this workflow job and is starting execution.
func (c *Client) MarkWorkflowJobRunning(ctx context.Context, jobRunID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runner/workflow-jobs/%d/running", jobRunID), nil, nil, true)
}

// AppendWorkflowJobLog sends a single log line to the platform. Stream
// must be "stdout", "stderr", or "system". The platform appends the line
// to workflow_job_logs and the caller is expected to call this once per
// output line from a step.
func (c *Client) AppendWorkflowJobLog(ctx context.Context, jobRunID int64, stream, line string) error {
	req := workflowJobLogRequest{Stream: stream, Line: line}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runner/workflow-jobs/%d/logs", jobRunID), req, nil, true)
}

type workflowJobLogRequest struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}

// WorkflowJobTerminateRequest reports a workflow job's terminal state.
type WorkflowJobTerminateRequest struct {
	Status   string `json:"status"`
	ExitCode int32  `json:"exit_code"`
	Message  string `json:"message,omitempty"`
}

// TerminateWorkflowJob reports the final status of a workflow job back
// to the platform. Status must be "success", "failed", or "cancelled".
func (c *Client) TerminateWorkflowJob(ctx context.Context, jobRunID int64, req WorkflowJobTerminateRequest) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runner/workflow-jobs/%d/terminate", jobRunID), req, nil, true)
}

// ---- message + input forwarding ----

type AppendMessageRequest struct {
	Kind       string          `json:"kind"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content,omitempty"`
	Phase      string          `json:"phase,omitempty"`
	Level      string          `json:"level,omitempty"`
	Msg        string          `json:"msg,omitempty"`
	Name       string          `json:"name,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCallDTO   `json:"tool_calls,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
}

type ToolCallDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (c *Client) AppendMessage(ctx context.Context, sessionID int64, req AppendMessageRequest) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/runner/sessions/%d/messages", sessionID), req, nil, true)
}

type InputsResponse struct {
	Frames []json.RawMessage `json:"frames"`
}

func (c *Client) PollInputs(ctx context.Context, sessionID int64) (*InputsResponse, error) {
	var out InputsResponse
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/runner/sessions/%d/inputs", sessionID), nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// HistoryResponse is the seed `kind:history` frame the agent's loop reads
// as its mandatory first inbound. Returned by GET /sessions/{id}/history
// — the runner calls this exactly once per agent process boot and writes
// Frame onto the container's stdin before starting the /inputs shipper.
type HistoryResponse struct {
	Frame json.RawMessage `json:"frame"`
}

// FetchHistory pulls the seed history frame for a session. The runner
// owns the responsibility of feeding the agent its first frame; the
// platform owns the contents of that frame (today always empty, M9
// will populate it from the message log). Keeping history off the
// /inputs queue means the agent's "first frame must be history"
// invariant survives crash + respawn, runner restart, and container
// reuse paths that the old enqueue-on-spawn design could not.
func (c *Client) FetchHistory(ctx context.Context, sessionID int64) (json.RawMessage, error) {
	var out HistoryResponse
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/runner/sessions/%d/history", sessionID), nil, &out, true); err != nil {
		return nil, err
	}
	if len(out.Frame) == 0 {
		return nil, fmt.Errorf("history: empty frame")
	}
	return out.Frame, nil
}

// ---- binary downloads ----

// DownloadBinary GETs a server-relative path (typically the URL field of
// a BootstrapPayload.Binaries entry) with the agent token attached, and
// returns the full body plus the server's advertised SHA256 from the
// X-Hangrix-SHA256 header. The header is empty when the upstream doesn't
// expose one; callers should always verify the body's own digest against
// the bootstrap-declared SHA before installing.
func (c *Client) DownloadBinary(ctx context.Context, path string) ([]byte, string, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if c.agentToken == "" {
		return nil, "", errors.New("agent token not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.agentToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("GET %s: %d %s", path, resp.StatusCode, snippet(body))
	}
	return body, resp.Header.Get("X-Hangrix-SHA256"), nil
}

// ---- transport helpers ----

func (c *Client) do(ctx context.Context, method, path string, in, out any, auth bool) error {
	body, status, err := c.raw(ctx, method, path, in, auth)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, status, snippet(body))
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func (c *Client) raw(ctx context.Context, method, path string, in any, auth bool) ([]byte, int, error) {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return nil, 0, fmt.Errorf("encode body: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		if c.agentToken == "" {
			return nil, 0, errors.New("agent token not set")
		}
		req.Header.Set("Authorization", "Bearer "+c.agentToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return out, resp.StatusCode, nil
}

func snippet(b []byte) string {
	if len(b) > 256 {
		return string(b[:256]) + "…"
	}
	return string(b)
}
