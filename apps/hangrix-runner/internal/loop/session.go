// Package loop drives one runner: heartbeats + task polling + per-session
// agent lifecycle. The split between this file and the per-session driver
// is deliberate — the outer Loop is "what does the runner do all day",
// the SessionDriver is "what happens to one container from claim to done".
package loop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// SessionDriver runs one claimed session end-to-end:
//
//	1. Resolve host paths (workdir, addendum file, bundle).
//	2. Start container via orchestrator.
//	3. Forward platform → agent (poll /inputs, write to stdin).
//	4. Forward agent → platform (read stdout lines, POST /messages).
//	5. Wait for exit, mark terminal.
//
// One driver per session; goroutines fan out internally and join before
// Run returns.
type SessionDriver struct {
	Client       *client.Client
	Orchestrator orchestrator.Orchestrator

	// Host paths the orchestrator binds into the container.
	AgentBinaryPath string
	WorkspaceRoot   string

	// Endpoints the in-container agent talks to. We merge these into the
	// env the platform sent, so the platform doesn't need to know what
	// hostname is reachable from inside the container.
	LLMEndpoint string
	MCPEndpoint string
}

// Run starts the container for the given task and stays in the IO loop
// until the container exits. Returns the exit code + an optional error.
// Never panics: any internal error is logged and converted into a
// terminal 'failed' session via the client.
func (d *SessionDriver) Run(ctx context.Context, task *client.Task) (exitCode int32, err error) {
	if err := d.Client.MarkRunning(ctx, task.SessionID); err != nil {
		log.Printf("session %d: mark running: %v", task.SessionID, err)
	}

	hostWorkdir := filepath.Join(d.WorkspaceRoot, fmt.Sprintf("session-%d", task.SessionID))
	hostAddendumPath := ""
	if task.HostAddendum != "" {
		path := filepath.Join(hostWorkdir, "host_addendum.md")
		if err := os.MkdirAll(hostWorkdir, 0o755); err != nil {
			return -1, d.fail(ctx, task.SessionID, fmt.Errorf("mkdir workdir: %w", err))
		}
		if err := os.WriteFile(path, []byte(task.HostAddendum), 0o600); err != nil {
			return -1, d.fail(ctx, task.SessionID, fmt.Errorf("write addendum: %w", err))
		}
		hostAddendumPath = path
	}

	env := buildAgentEnv(task, d.LLMEndpoint, d.MCPEndpoint)

	otask := orchestrator.Task{
		SessionID:        task.SessionID,
		Image:            task.AgentImage,
		AgentBinaryPath:  d.AgentBinaryPath,
		HostBundleDir:    task.BundleDir,
		HostAddendumPath: hostAddendumPath,
		HostWorkdir:      hostWorkdir,
		Env:              env,
	}
	handle, err := d.Orchestrator.Start(ctx, otask)
	if err != nil {
		return -1, d.fail(ctx, task.SessionID, fmt.Errorf("start container: %w", err))
	}

	// IO fan-out. Three goroutines:
	//   * stdin shipper:  poll /inputs → write to container stdin
	//   * stdout drain:   read container stdout → POST /messages
	//   * stderr drain:   read container stderr → POST /messages (log kind)
	var wg sync.WaitGroup
	ioCtx, cancelIO := context.WithCancel(ctx)
	defer cancelIO()

	wg.Add(3)
	go func() { defer wg.Done(); d.shipStdin(ioCtx, task.SessionID, handle.Stdin()) }()
	go func() { defer wg.Done(); d.shipStdout(ioCtx, task.SessionID, handle.Stdout()) }()
	go func() { defer wg.Done(); d.shipStderr(ioCtx, task.SessionID, handle.Stderr()) }()

	ec, waitErr := handle.Wait()
	cancelIO()
	wg.Wait()

	exitCode = int32(ec)
	status := client.TerminateRequest{Status: "succeeded", ExitCode: &exitCode}
	if waitErr != nil || ec != 0 {
		status.Status = "failed"
		if waitErr != nil {
			status.Message = waitErr.Error()
		}
	}
	if err := d.Client.Terminate(ctx, task.SessionID, status); err != nil {
		log.Printf("session %d: terminate: %v", task.SessionID, err)
	}
	return exitCode, waitErr
}

// fail is the short-circuit path when we can't even start the container.
// Reports the failure to the platform and returns the wrapped error so
// the caller can keep its own logs aligned.
func (d *SessionDriver) fail(ctx context.Context, sessionID int64, e error) error {
	code := int32(-1)
	if err := d.Client.Terminate(ctx, sessionID, client.TerminateRequest{
		Status:   "failed",
		ExitCode: &code,
		Message:  e.Error(),
	}); err != nil {
		log.Printf("session %d: terminate-on-fail: %v", sessionID, err)
	}
	return e
}

// shipStdin polls the platform for inbound IPC frames and writes each one
// (terminated by '\n') to the container stdin. Exits on context cancel
// or a write error (container exit closes the pipe).
func (d *SessionDriver) shipStdin(ctx context.Context, sessionID int64, w io.WriteCloser) {
	defer w.Close()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		resp, err := d.Client.PollInputs(ctx, sessionID)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("session %d: poll inputs: %v", sessionID, err)
			time.Sleep(time.Second)
			continue
		}
		for _, frame := range resp.Frames {
			if _, err := w.Write(append([]byte(frame), '\n')); err != nil {
				return
			}
		}
	}
}

// shipStdout reads JSON-Lines off the container's stdout and forwards
// each frame to the platform as an /messages append. Unknown kinds are
// preserved verbatim — the platform validates.
func (d *SessionDriver) shipStdout(ctx context.Context, sessionID int64, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var frame outboundFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			d.appendLog(ctx, sessionID, "warn", "agent emitted non-JSON stdout: "+string(line))
			continue
		}
		req := frame.toAppendRequest()
		if err := d.Client.AppendMessage(ctx, sessionID, req); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("session %d: append message: %v", sessionID, err)
		}
	}
}

// shipStderr forwards stderr lines as log frames. The agent in M6b
// writes startup banners + recover-on-panic lines to stderr; capturing
// them on the platform side keeps the diagnostics in one place.
func (d *SessionDriver) shipStderr(ctx context.Context, sessionID int64, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 32*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		d.appendLog(ctx, sessionID, "info", "stderr: "+line)
	}
}

func (d *SessionDriver) appendLog(ctx context.Context, sessionID int64, level, msg string) {
	_ = d.Client.AppendMessage(ctx, sessionID, client.AppendMessageRequest{
		Kind:  "log",
		Level: level,
		Msg:   msg,
	})
}

// outboundFrame mirrors apps/hangrix-agent/internal/ipc.Outbound on the
// wire. We deliberately keep our own copy here (instead of importing the
// agent package) so the runner binary doesn't transitively pull the
// agent's third-party deps.
type outboundFrame struct {
	Kind       string          `json:"kind"`
	Phase      string          `json:"phase,omitempty"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content,omitempty"`
	ToolCalls  []toolCall      `json:"tool_calls,omitempty"`
	Name       string          `json:"name,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Level      string          `json:"level,omitempty"`
	Msg        string          `json:"msg,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
}

type toolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (f outboundFrame) toAppendRequest() client.AppendMessageRequest {
	req := client.AppendMessageRequest{
		Kind:       f.Kind,
		Role:       f.Role,
		Content:    f.Content,
		Phase:      f.Phase,
		Level:      f.Level,
		Msg:        f.Msg,
		Name:       f.Name,
		Args:       f.Args,
		Result:     f.Result,
		ToolCallID: f.ToolCallID,
		TurnID:     f.TurnID,
	}
	if len(f.ToolCalls) > 0 {
		req.ToolCalls = make([]client.ToolCallDTO, len(f.ToolCalls))
		for i, c := range f.ToolCalls {
			req.ToolCalls[i] = client.ToolCallDTO{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
		}
	}
	return req
}

// buildAgentEnv assembles the HANGRIX_* env vars the agent expects. We
// start from whatever the platform sent (its ExtraEnv plus any role
// hints), then layer the runner-side overrides on top so the in-container
// agent can reach LLM / MCP endpoints reachable from the runner's network.
func buildAgentEnv(task *client.Task, llmEndpoint, mcpEndpoint string) map[string]string {
	env := map[string]string{}
	for k, v := range task.Env {
		env[k] = v
	}
	if task.SessionToken != "" {
		env["HANGRIX_SESSION_TOKEN"] = task.SessionToken
	}
	if llmEndpoint != "" {
		env["HANGRIX_LLM_ENDPOINT"] = llmEndpoint
	}
	if mcpEndpoint != "" {
		env["HANGRIX_PLATFORM_MCP_ENDPOINT"] = mcpEndpoint
	}
	env["HANGRIX_SESSION_ID"] = strconv.FormatInt(task.SessionID, 10)
	if task.Role != "" {
		env["HANGRIX_ROLE"] = task.Role
	}
	if task.WorkingBranch != "" {
		env["HANGRIX_WORKING_BRANCH"] = task.WorkingBranch
	}
	if task.BaseBranch != "" {
		env["HANGRIX_BASE_BRANCH"] = task.BaseBranch
	}
	return env
}
