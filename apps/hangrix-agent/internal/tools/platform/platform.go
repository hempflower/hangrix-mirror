// Package platform implements the agent-side "platform tools" as
// built-ins. Each tool maps to a specific v1 REST endpoint under
// `/api/v1/...` and handles the v1 response envelope
// ({"data":...}, {"message":"...","errors":[...]}, 204 No Content)
// rather than the legacy {is_error,text} RPC shape.
//
// Tools are hardcoded descriptors (name + description + JSON-Schema)
// that must match the server's service-layer definitions. A drift
// surfaces as a 4xx on the first call — not silent.
//
// The Client handles authentication, retry policy, and response
// decoding centrally. Individual tools specify only their endpoint
// metadata (method, path builder, optional query/body transforms).
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/httpx"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

// Client is the shared HTTP backend every platform tool uses. One per
// process; goroutine-safe.
type Client struct {
	baseURL string
	token   string
	http    *http.Client

	// Lazy /me cache for issue_create repo-scope resolution.
	meOnce   sync.Once
	meRepoID int64
	meErr    error
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpx.NewClient(60 * time.Second),
	}
}

// repoID returns the session's repo ID, lazily fetched from GET /api/v1/me.
// Cached for the lifetime of the Client (one process).
func (c *Client) repoID(ctx context.Context) (int64, error) {
	c.meOnce.Do(func() {
		var resp v1Singleton
		err := c.doV1(ctx, http.MethodGet, "/me", "", nil, false, &resp)
		if err != nil {
			c.meErr = err
			return
		}
		var me struct {
			SessionID     int64  `json:"session_id"`
			RoleKey       string `json:"role_key"`
			RepoID        *int64 `json:"repo_id"`
			IssueNumber   *int32 `json:"issue_number"`
			SessionStatus string `json:"session_status"`
			TokenActive   bool   `json:"token_active"`
		}
		if err := json.Unmarshal(resp.Data, &me); err != nil {
			c.meErr = fmt.Errorf("decode /me response: %w", err)
			return
		}
		if me.RepoID == nil {
			c.meErr = errors.New("/me: repo_id is nil — session may not be scoped to a repo")
			return
		}
		c.meRepoID = *me.RepoID
	})
	return c.meRepoID, c.meErr
}

// ---- v1 response types ----

// v1Singleton is the standard v1 success envelope: {"data": ...}.
type v1Singleton struct {
	Data json.RawMessage `json:"data"`
}

// v1Error is the standard v1 error envelope.
type v1Error struct {
	Message string       `json:"message"`
	Errors  []v1FieldErr `json:"errors,omitempty"`
}

type v1FieldErr struct {
	Resource string `json:"resource,omitempty"`
	Field    string `json:"field,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message,omitempty"`
}

// ---- low-level HTTP ----

// doV1 sends a request to baseURL+path and decodes the v1 JSON envelope.
// For 204 responses, out is left as-is (typically nil).
// expect204: when true, 204 is treated as success (out is unchanged).
func (c *Client) doV1(ctx context.Context, method, path, query string, body []byte, expect204 bool, out *v1Singleton) error {
	url := c.baseURL + path
	if query != "" {
		url += "?" + query
	}

	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(math.Pow(2, float64(attempt-1))*500) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		// 204 No Content — success with no body.
		if resp.StatusCode == http.StatusNoContent {
			if expect204 {
				return nil
			}
			return nil // treat as success
		}

		// 5xx — retry.
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("platform: http %d %s: %s", resp.StatusCode, path, truncate(respBody, 256))
			continue
		}

		// 4xx — terminal, return structured error.
		if resp.StatusCode >= 400 {
			return decodeV1Error(resp.StatusCode, path, respBody)
		}

		// 2xx — decode singleton envelope.
		if out != nil {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("platform: decode %s response: %w", path, err)
			}
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("platform: exhausted retries")
	}
	return lastErr
}

// doV1Multipart sends a multipart/form-data POST to baseURL+path.
func (c *Client) doV1Multipart(ctx context.Context, path string, body []byte, contentType string, out *v1Singleton) error {
	url := c.baseURL + path

	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(math.Pow(2, float64(attempt-1))*500) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("platform: http %d %s: %s", resp.StatusCode, path, truncate(respBody, 256))
			continue
		}
		if resp.StatusCode >= 400 {
			return decodeV1Error(resp.StatusCode, path, respBody)
		}
		if out != nil {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("platform: decode %s response: %w", path, err)
			}
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("platform: exhausted retries")
	}
	return lastErr
}

// decodeV1Error converts a v1 error response into a structured error the
// LLM can understand. The error value is a serialised JSON object so the
// runtime can surface it as {is_error,status,error,details} rather than
// a raw Go error string.
func decodeV1Error(status int, path string, body []byte) error {
	var ve v1Error
	if err := json.Unmarshal(body, &ve); err != nil || ve.Message == "" {
		// Fallback: use raw body as message.
		msg := string(body)
		if msg == "" {
			msg = http.StatusText(status)
		}
		ve = v1Error{Message: msg}
	}
	// Serialise back so the caller gets a JSON string it can parse.
	payload := map[string]any{
		"is_error": true,
		"status":   status,
		"error":    ve.Message,
	}
	if len(ve.Errors) > 0 {
		payload["details"] = ve.Errors
	}
	out, _ := json.Marshal(payload)
	return fmt.Errorf("%s", out)
}

// ---- path builders ----

// staticPath returns a path builder that always returns the given path.
func staticPath(p string) func(json.RawMessage) (string, error) {
	return func(_ json.RawMessage) (string, error) { return p, nil }
}

// paramPath returns a path builder that replaces "{key}" placeholders
// in the template with values extracted from args[key].
func paramPath(template string, keys ...string) func(json.RawMessage) (string, error) {
	return func(args json.RawMessage) (string, error) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(args, &m); err != nil {
			return "", fmt.Errorf("parse args for path params: %w", err)
		}
		result := template
		for _, key := range keys {
			raw, ok := m[key]
			if !ok {
				return "", fmt.Errorf("missing required path param %q", key)
			}
			// Unwrap JSON string/int values.
			var val string
			if err := json.Unmarshal(raw, &val); err == nil {
				result = strings.ReplaceAll(result, "{"+key+"}", val)
				continue
			}
			var intVal int64
			if err := json.Unmarshal(raw, &intVal); err == nil {
				result = strings.ReplaceAll(result, "{"+key+"}", strconv.FormatInt(intVal, 10))
				continue
			}
			return "", fmt.Errorf("path param %q must be string or integer, got %s", key, string(raw))
		}
		return result, nil
	}
}

// queryBuilder returns a function that builds a query string from args
// for the given key names (booleans encoded as "true"/"false").
func queryBuilder(keys ...string) func(json.RawMessage) string {
	return func(args json.RawMessage) string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(args, &m); err != nil {
			return ""
		}
		var parts []string
		for _, key := range keys {
			raw, ok := m[key]
			if !ok {
				continue
			}
			var val string
			if err := json.Unmarshal(raw, &val); err == nil {
				parts = append(parts, key+"="+val)
				continue
			}
			var boolVal bool
			if err := json.Unmarshal(raw, &boolVal); err == nil {
				if boolVal {
					parts = append(parts, key+"=true")
				}
				continue
			}
		}
		return strings.Join(parts, "&")
	}
}

// ---- Tool types ----

// Tool implements local.Tool for a standard v1 JSON endpoint.
type Tool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client

	method      string
	buildPath   func(json.RawMessage) (string, error)
	buildQuery  func(json.RawMessage) string
	expect204   bool
}

func (t *Tool) Name() string           { return t.name }
func (t *Tool) Description() string    { return t.description }
func (t *Tool) Schema() map[string]any { return t.schema }

// Call executes the tool's v1 endpoint and returns the decoded response.
// Semantic errors (4xx) are returned as structured {is_error,status,error}
// objects so the LLM sees the reason, not as Go errors that would abort
// the turn. Transport / 5xx errors become Go errors.
func (t *Tool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage(`{}`)
	}

	path, err := t.buildPath(args)
	if err != nil {
		return nil, err
	}
	query := ""
	if t.buildQuery != nil {
		query = t.buildQuery(args)
	}

	var resp v1Singleton
	err = t.client.doV1(ctx, t.method, path, query, args, t.expect204, &resp)
	if err != nil {
		// If it's a structured v1 error (starts with {"is_error"), return
		// it as a parsed value so the LLM sees the semantic failure.
		msg := err.Error()
		if strings.HasPrefix(msg, "{") {
			var parsed any
			if json.Unmarshal([]byte(msg), &parsed) == nil {
				return parsed, nil
			}
		}
		return nil, err
	}
	if t.expect204 || len(resp.Data) == 0 {
		return map[string]any{"ok": true}, nil
	}
	var parsed any
	if err := json.Unmarshal(resp.Data, &parsed); err == nil {
		return parsed, nil
	}
	return resp.Data, nil
}

// attachmentTool is like Tool but sends a multipart/form-data upload.
type attachmentTool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client
}

func (t *attachmentTool) Name() string           { return t.name }
func (t *attachmentTool) Description() string    { return t.description }
func (t *attachmentTool) Schema() map[string]any { return t.schema }

const maxAttachmentBytes = 64 << 20
const workspaceRoot = "/workspace"

// resolveWorkspacePath resolves a user-supplied path to a real,
// symlink-resolved absolute path that MUST fall within workspaceRoot.
func resolveWorkspacePath(p string) (string, error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(workspaceRoot, p)
	}
	if !strings.HasPrefix(p, workspaceRoot+"/") && p != workspaceRoot {
		return "", fmt.Errorf("path %q is outside workspace", p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", p, err)
	}
	rel, err := filepath.Rel(workspaceRoot, resolved)
	if err != nil {
		return "", fmt.Errorf("path %q resolves outside workspace", p)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q resolves outside workspace", p)
	}
	return resolved, nil
}

func (t *attachmentTool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	var req struct {
		Path        string `json:"path"`
		DisplayName string `json:"display_name"`
		Inline      bool   `json:"inline"`
		CommentID   int64  `json:"comment_id"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("issue_attachment_upload: parse arguments: %w. Pass a JSON object with at least \"path\".", err)
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		return nil, fmt.Errorf("issue_attachment_upload: path is required. Provide a workspace-relative or absolute path to the file to upload.")
	}

	safePath, err := resolveWorkspacePath(req.Path)
	if err != nil {
		return nil, fmt.Errorf("issue_attachment_upload: %w. Only files inside the workspace can be uploaded.", err)
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return nil, fmt.Errorf("issue_attachment_upload: cannot read file %q: %w. Check that the path exists and is a regular file in the workspace.", req.Path, err)
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("issue_attachment_upload: file %q is %d bytes, exceeds the %d MiB limit. Consider compressing or splitting the file before uploading.", req.Path, len(data), maxAttachmentBytes>>20)
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	fw, err := w.CreateFormFile("file", filepath.Base(req.Path))
	if err != nil {
		return nil, fmt.Errorf("issue_attachment_upload: build multipart form: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return nil, fmt.Errorf("issue_attachment_upload: write file part: %w", err)
	}
	if req.DisplayName != "" {
		_ = w.WriteField("display_name", req.DisplayName)
	}
	_ = w.WriteField("inline", strconv.FormatBool(req.Inline))
	if req.CommentID != 0 {
		_ = w.WriteField("comment_id", strconv.FormatInt(req.CommentID, 10))
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("issue_attachment_upload: close multipart writer: %w", err)
	}

	var resp v1Singleton
	err = t.client.doV1Multipart(ctx, "/issues/current/attachments", body.Bytes(), w.FormDataContentType(), &resp)
	if err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "{") {
			var parsed any
			if json.Unmarshal([]byte(msg), &parsed) == nil {
				return parsed, nil
			}
		}
		return nil, err
	}
	if len(resp.Data) == 0 {
		return map[string]any{"ok": true}, nil
	}
	var parsed any
	if err := json.Unmarshal(resp.Data, &parsed); err == nil {
		return parsed, nil
	}
	return resp.Data, nil
}

// todoUpdateTool handles issue_todo_update with split dispatch:
// todo_id==0 → POST /todos (create), todo_id!=0 → PATCH /todos/{id} (update).
type todoUpdateTool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client
}

func (t *todoUpdateTool) Name() string           { return t.name }
func (t *todoUpdateTool) Description() string    { return t.description }
func (t *todoUpdateTool) Schema() map[string]any { return t.schema }

func (t *todoUpdateTool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage(`{}`)
	}

	// Determine create vs update based on todo_id.
	var probe struct {
		TodoID int64 `json:"todo_id"`
	}
	json.Unmarshal(args, &probe) // best-effort parse

	var method, path string
	if probe.TodoID != 0 {
		method = http.MethodPatch
		path = fmt.Sprintf("/issues/current/todos/%d", probe.TodoID)
	} else {
		method = http.MethodPost
		path = "/issues/current/todos"
	}

	var resp v1Singleton
	err := t.client.doV1(ctx, method, path, "", args, false, &resp)
	if err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "{") {
			var parsed any
			if json.Unmarshal([]byte(msg), &parsed) == nil {
				return parsed, nil
			}
		}
		return nil, err
	}
	var parsed any
	if err := json.Unmarshal(resp.Data, &parsed); err == nil {
		return parsed, nil
	}
	return resp.Data, nil
}

// createIssueTool handles issue_create, which needs to resolve the repo
// ID from GET /api/v1/me before calling POST /api/v1/repos/{repoID}/issues.
type createIssueTool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client
}

func (t *createIssueTool) Name() string           { return t.name }
func (t *createIssueTool) Description() string    { return t.description }
func (t *createIssueTool) Schema() map[string]any { return t.schema }

func (t *createIssueTool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage(`{}`)
	}
	repoID, err := t.client.repoID(ctx)
	if err != nil {
		return nil, fmt.Errorf("issue_create: resolve repo id: %w", err)
	}
	path := fmt.Sprintf("/repos/%d/issues", repoID)

	var resp v1Singleton
	err = t.client.doV1(ctx, http.MethodPost, path, "", args, false, &resp)
	if err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "{") {
			var parsed any
			if json.Unmarshal([]byte(msg), &parsed) == nil {
				return parsed, nil
			}
		}
		return nil, err
	}
	var parsed any
	if err := json.Unmarshal(resp.Data, &parsed); err == nil {
		return parsed, nil
	}
	return resp.Data, nil
}

// questionnaireTool handles ask_question: creates a questionnaire via
// POST /api/v1/issues/current/questionnaires and — when
// wait_for_first_answer is true — schedules a timeout notification via
// async.ScheduleWithID. It returns immediately with status="scheduled",
// exactly like the sleep tool. The runtime loop's batch gate prevents
// the LLM from chaining other tool calls in the same batch.
//
// check_questionnaire and close_questionnaire are plain synchronous
// platform tools and use the standard Tool / toolDef path.
type questionnaireTool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client
	async       local.AsyncLifecycle
}

func (t *questionnaireTool) Name() string           { return t.name }
func (t *questionnaireTool) Description() string    { return t.description }
func (t *questionnaireTool) Schema() map[string]any { return t.schema }

func (t *questionnaireTool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage(`{}`)
	}

	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Questions   []any  `json:"questions"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("ask_question: parse arguments: %w", err)
	}

	// POST to create the questionnaire.
	createBody, err := json.Marshal(map[string]any{
		"title":       req.Title,
		"description": req.Description,
		"questions":   req.Questions,
	})
	if err != nil {
		return nil, fmt.Errorf("ask_question: marshal create body: %w", err)
	}

	var resp v1Singleton
	if err := t.client.doV1(ctx, http.MethodPost, "/issues/current/questionnaires", "", createBody, false, &resp); err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "{") {
			var parsed any
			if json.Unmarshal([]byte(msg), &parsed) == nil {
				return parsed, nil
			}
		}
		return nil, err
	}

	// Extract the questionnaire_id from the response. The server returns
	// the full questionnaire object in data, which includes an "id" field.
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		return nil, fmt.Errorf("ask_question: decode create response: %w", err)
	}
	qID := created.ID

	// Always schedule an infinite wait — the agent parks until woken by a
	// questionnaire.answered / questionnaire.closed event. The duration
	// is effectively infinite (math.MaxInt64 ≈ 292 years); CancelSchedule
	// cleans it up when a relevant event arrives.
	if t.async != nil {
		t.async.ScheduleWithID(fmt.Sprintf("questionnaire-%d", qID), time.Duration(math.MaxInt64), "")
	}

	// Decode the response data to include in the result.
	var createdFull any
	json.Unmarshal(resp.Data, &createdFull)

	result := map[string]any{
		"status":           "scheduled",
		"questionnaire_id": qID,
		"note":             "Returned immediately. End the current turn — a notification will wake you when an answer arrives. Use check_questionnaire to poll results and close_questionnaire to close it.",
	}
	if createdFull != nil {
		result["questionnaire"] = createdFull
	}
	return result, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// ---- Tool catalogue ----

// AskQuestionToolName is the name of the ask_question platform tool,
// referenced by the runtime loop's batch gate so it can enforce
// "no other tool calls in the same batch as ask_question" (same pattern
// as sleep).
const AskQuestionToolName = "ask_question"

// All returns every platform tool the agent ships with, bound to the
// supplied HTTP client and async lifecycle. Order matters for catalogue
// stability — keep read-only tools first then mutating ones.
//
// When readOnly is true (HANGRIX_REPO_PERMISSION == "read"), every tool
// whose def is marked write:true is skipped entirely — it never appears
// in the returned slice or the LLM's tool schema. Read tools are always
// included.
//
// async is the AsyncLifecycle handle (the bashTool instance) used by
// ask_question to schedule timeout notifications. It may be nil in test
// contexts where background scheduling is not needed.
func All(client *Client, async local.AsyncLifecycle, readOnly bool) []local.Tool {
	if client == nil {
		return nil
	}

	type toolDef struct {
		name        string
		description string
		schema      map[string]any
		kind        string // "get", "post", "patch", "delete", "attachment", "todo", "create_issue"
		path        string // static path for simple tools
		method      string // only needed if kind doesn't imply it
		pathParams  []string
		queryParams []string
		expect204   bool
		write       bool // mutating tool; hidden when readOnly is true
	}

	defs := []toolDef{
		// ---- read-only tools ----
		{name: "issue_read", description: "Read the current issue's metadata, comments, and timeline events.", kind: "get", path: "/issues/current"},
		{name: "issue_comment_read", description: "Read a single comment by its id. Only comments on the current session's issue are accessible — cross-issue lookups return 'not found'. Returns the full body (no truncation).",
			kind: "get", path: "/issues/current/comments/{comment_id}", pathParams: []string{"comment_id"},
			schema: objectSchema(map[string]any{"comment_id": intProp("The comment id to read (required). Must belong to the current session's issue.")}, []string{"comment_id"}),
		},
		{name: "issue_mergeable", description: "Check whether the issue branch can be merged into its base — tries fast-forward first, then checks for conflicts. mergeable=true means issue_merge is expected to succeed. When conflicted (mode=conflicted), the hint explains to resolve it through a new contribution branch rather than pushing the issue branch directly. Returns mergeable, mode, base_branch, base_sha, head_sha, hint, and incomplete_todos / incomplete_sub_issues when either blocks the merge.",
			kind: "get", path: "/issues/current/mergeability",
		},
		{name: "issue_todo_list", description: "List todos for the current issue. Returns a lightweight `todos` array and `todo_summary` (total, todo, in_progress, done, all_done) without pulling full issue metadata.",
			kind: "get", path: "/issues/current/todos",
		},
		{name: "issue_children", description: "List sub-issues (child issues) of the current issue.",
			kind: "get", path: "/issues/current/children",
		},
		{name: "issue_read_by_number", description: "Read an issue by its number (e.g. 91). Returns the issue's metadata, comments, and timeline events. Only works for issues within the same repository as the current session.",
			kind: "get", path: "/issues/{issue_number}", pathParams: []string{"issue_number"},
			schema: objectSchema(map[string]any{"issue_number": intProp("The issue number to read (required, e.g. 91). Must belong to the same repository as the current session.")}, []string{"issue_number"}),
		},
		{name: "issue_checks", description: "List the latest state of each CI check on the issue's head commit. Currently always returns [].",
			kind: "get", path: "/issues/current/checks",
		},
		{name: "roster_list", description: "List every active role session on the current issue. Each item includes a `last_activity_at` field showing the most recent activity timestamp for that session — use it to detect stalled agents.",
			kind: "get", path: "/issues/current/sessions",
		},
		{name: "contribution_list", description: "List the contribution branches on the current issue. Each entry has id, agent_role, actor (with kind, id, display_name, role_key), ref_name, status (pending/approved/rejected/merged/closed), mergeable, merge_mode, head_sha, and diff stats. By default excludes closed and merged contributions. Use include_closed and include_merged to optionally include them. A contribution is created automatically when you push to issue-<N>/<your-role>/<slug>.",
			kind: "get", path: "/issues/current/contributions", queryParams: []string{"include_closed", "include_merged"},
			schema: objectSchema(map[string]any{
				"include_closed": boolProp("When true, include contributions with status 'closed' in the results. Default: false (closed contributions are excluded)."),
				"include_merged": boolProp("When true, include contributions with status 'merged' in the results. Default: false (merged contributions are excluded)."),
			}, nil),
		},
		{name: "contribution_read", description: "Read one contribution: metadata (id, agent_role, actor with kind/id/display_name/role_key, ref_name, status, mergeable, merge_mode, head_sha, diff stats), review status (verdict, required reviewers still pending), and a checkout_hint with the branch ref and issue branch names so you can fetch the contribution branch locally and inspect its diff with git. Use the id from contribution_list.",
			kind: "get", path: "/issues/current/contributions/{id}", pathParams: []string{"id"},
			schema: objectSchema(map[string]any{"id": intProp("Contribution id to read (from contribution_list).")}, []string{"id"}),
		},

		// ---- mutating tools ----
		{name: "issue_create", description: "Create a new issue in the current repo. Set `parent: true` to create as a sub-issue of the current issue.",
			kind: "create_issue", write: true,
			schema: objectSchema(map[string]any{
				"title":  stringProp("Issue title (1-200 characters)."),
				"body":   stringProp("Optional issue body (markdown)."),
				"parent": boolProp("When true, creates the new issue as a sub-issue of the current issue. Default: false."),
			}, []string{"title"}),
		},
		{name: "issue_comment", description: "Post a comment on the current issue. `body` is markdown; @agent-<role-key> mentions wake other roles.",
			kind: "post", path: "/issues/current/comments", write: true,
			schema: objectSchema(map[string]any{
				"body": stringPropMax(
					"The comment body. Markdown allowed; mentions follow @agent-<role-key> grammar. "+
						"Maximum 7800 Unicode characters (runes). If your content exceeds the limit, "+
						"split it across multiple issue_comment calls and prefix each with `[1/N]`, `[2/N]`, …",
					7800,
				),
				"file_path": stringProp("Optional path to anchor the comment to a file (inline review). Omit for top-level."),
				"line":      intProp("Optional line number to anchor inline. Requires file_path."),
			}, []string{"body"}),
		},
		{name: "issue_comment_cross", description: "Post a comment on another issue within the same repo. The target must be the caller's parent or child issue (direct lineage). Self-target is rejected. Use for cross-issue communication — @agent-<role-key> mentions only wake agents on the same issue; use this tool to reach a parent or child issue's agents instead.",
			kind: "post", path: "/issues/{targetIssueNumber}/comments", pathParams: []string{"targetIssueNumber"}, write: true,
			schema: objectSchema(map[string]any{
				"targetIssueNumber": intProp("The issue number to post the comment to (required). Must be a parent or child of the current issue."),
				"body": stringPropMax(
					"The comment body. Markdown allowed; mentions follow @agent-<role-key> grammar. "+
						"Maximum 7800 Unicode characters (runes). If your content exceeds the limit, "+
						"split it across multiple issue_comment_cross calls and prefix each with `[1/N]`, `[2/N]`, …",
					7800,
				),
				"file_path": stringProp("Optional path to anchor the comment to a file (inline review). Omit for top-level."),
				"line":      intProp("Optional line number to anchor inline. Requires file_path."),
			}, []string{"targetIssueNumber", "body"}),
		},
		{name: "issue_edit", description: "Edit the current issue's title and/or body. At least one of `title` or `body` must be provided. When the title changes a `title_changed` event is written to the timeline; a body-only edit is silent. Title must be non-empty and ≤200 characters.",
			kind: "patch", path: "/issues/current", write: true,
			schema: issueEditSchema(),
		},
		{name: "issue_review_vote", description: "Cast a structured review vote on a contribution branch (approve / reject / abstain). A branch is approved once every required reviewer votes approve/abstain; any reject rejects it. Pass the contribution_id from contribution_list; you cannot approve your own contribution.",
			kind: "post", path: "/issues/current/reviews", write: true,
			schema: objectSchema(map[string]any{
				"contribution_id": intProp("The contribution branch this vote targets (from contribution_list)."),
				"value":           enumProp("Vote outcome. reject means the author should revise via a new versioned branch.", []string{"approve", "reject", "abstain"}),
				"reason":          stringProp("Free-text rationale shown on the timeline. Recommended even for 'approve'."),
			}, []string{"contribution_id", "value"}),
		},
		{name: "issue_close", description: "Close the current issue without merging. Archives every active agent session on it. Blocked if any sub-issue is still open.",
			kind: "post", path: "/issues/current/close", write: true,
			schema: objectSchema(map[string]any{"reason": stringProp("Optional rationale, recorded on the timeline.")}, nil),
		},
		{name: "issue_merge", description: "Merge the issue branch into its base — tries fast-forward first, falls back to auto-rebase. Fails if there are no commits or a rebase conflict. Blocked if any sub-issue is still open or any todo is unfinished.",
			kind: "post", path: "/issues/current/merge", write: true,
			schema: objectSchema(map[string]any{"message": stringProp("Optional merge message.")}, nil),
		},
		{name: "session_recover", description: "Recover a failed / succeeded / cancelled / idle session on the current issue. Sets it back to pending so the runner picks it up. Restricted to sessions on the same issue.",
			kind: "post", path: "/issues/current/sessions/{session_id}/recover", pathParams: []string{"session_id"}, write: true,
			schema: objectSchema(map[string]any{"session_id": intProp("The session ID to recover. Must be on the same issue as the caller.")}, []string{"session_id"}),
		},
		{name: "issue_attachment_upload", description: "Upload a file from the workspace as an issue attachment. Returns attachment metadata including an `attachment_id`, `url`, and `markdown_snippet` — use `issue_comment` to insert the snippet into a comment body. `path` must be a workspace-relative or absolute path to an existing file. Set `inline` to true for images/videos you want rendered inline (produces `![](url)` syntax); false / omitted produces `[name](url)` link syntax.",
			kind: "attachment", write: true,
			schema: objectSchema(map[string]any{
				"path":         stringProp("Workspace-relative or absolute path to the file to upload. Required."),
				"display_name": stringProp("Optional display name for the attachment. Defaults to the file's basename."),
				"inline":       boolProp("When true, produces inline syntax `![](url)` for images/videos. Default false."),
				"comment_id":   intProp("Optional comment ID to bind the attachment to an existing comment."),
			}, []string{"path"}),
		},
		{name: "issue_todo_update", description: "Create or update a todo on the current issue. Omit `todo_id` (or pass 0) to create a new todo — `content` is required in that case. Pass a non-zero `todo_id` to update an existing todo's `status` and/or `content`. `status` must be one of: todo, in_progress, done. Returns the created/updated todo object on success.",
			kind: "todo", write: true,
			schema: objectSchema(map[string]any{
				"todo_id":  intProp("The todo id to update. Omit or pass 0 to create a new todo instead."),
				"status":   enumProp("New status. One of: todo, in_progress, done. Use todo when creating.", []string{"todo", "in_progress", "done"}),
				"content":  stringProp("Todo text. Required when creating; optional when updating."),
				"position": intProp("Optional position for ordering (new todos only). Default 0."),
			}, []string{"status"}),
		},
		{name: "contribution_set_meta", description: "Set the title and description of your own contribution branch (its merge-request title/body). Only the role that owns the branch may set its metadata.",
			kind: "patch", path: "/issues/current/contributions/{id}", pathParams: []string{"id"}, write: true,
			schema: objectSchema(map[string]any{
				"id":          intProp("Contribution id."),
				"title":       stringProp("Short title (1-200 chars)."),
				"description": stringProp("Optional longer description."),
			}, []string{"id", "title"}),
		},
		{name: "contribution_apply", description: "Merge an approved contribution branch into the issue branch (first-level gate). The server validates the review gate (status must be approved) + mergeability and computes the merge commit — there is no agent push. Requires `contribution_apply` in the role's `can:` whitelist (the maintainer).",
			kind: "post", path: "/issues/current/contributions/{id}/apply", pathParams: []string{"id"}, write: true,
			schema: objectSchema(map[string]any{
				"id":      intProp("Contribution id to merge (from contribution_list)."),
				"message": stringProp("Optional merge commit message."),
			}, []string{"id"}),
		},
		{name: "contribution_close", description: "Close (abandon) your own contribution branch. Only the owning role may close it; merged contributions cannot be closed.",
			kind: "post", path: "/issues/current/contributions/{id}/close", pathParams: []string{"id"}, write: true,
			schema: objectSchema(map[string]any{
				"id":     intProp("Contribution id to close."),
				"reason": stringProp("Optional rationale, recorded on the timeline."),
			}, []string{"id"}),
		},
		{name: "release_create", description: "Create a new release in draft state from an existing git tag. The tag must already exist in the repo.",
			kind: "post", path: "/releases", write: true,
			schema: objectSchema(map[string]any{
				"tag_name": stringProp("The existing git tag to create the release from (required)."),
				"title":    stringProp("Optional release title. Defaults to the tag name if omitted."),
				"notes":    stringProp("Optional release notes (markdown)."),
			}, []string{"tag_name"}),
		},
		{name: "release_upload_asset", description: "Upload a custom asset to a release. The file content must be base64-encoded.",
			kind: "post", path: "/releases/{release_id}/assets", pathParams: []string{"release_id"}, write: true,
			schema: objectSchema(map[string]any{
				"release_id":   intProp("The release ID to attach the asset to (required)."),
				"name":         stringProp("Asset file name (required)."),
				"content":      stringProp("Base64-encoded file content (required)."),
				"content_type": stringProp("Optional MIME type. Defaults to application/octet-stream."),
			}, []string{"release_id", "name", "content"}),
		},
		{name: "release_publish", description: "Publish a draft release, making it visible as an official release with a published_at timestamp.",
			kind: "post", path: "/releases/{release_id}/publish", pathParams: []string{"release_id"}, write: true,
			schema: objectSchema(map[string]any{"release_id": intProp("The release ID to publish (required).")}, []string{"release_id"}),
		},
		{name: "release_update", description: "Edit an existing release's metadata (title, notes). The tag_name can only be changed while the release is still a draft.",
			kind: "patch", path: "/releases/{release_id}", pathParams: []string{"release_id"}, write: true,
			schema: objectSchema(map[string]any{
				"release_id": intProp("The release ID to update (required)."),
				"title":      stringProp("Optional new release title."),
				"notes":      stringProp("Optional new release notes (markdown)."),
				"tag_name":   stringProp("Optional new tag name. Only mutable when the release is still a draft."),
			}, []string{"release_id"}),
		},
		{name: "release_delete", description: "Delete a release and all of its custom assets. Derived source archives (zip/tar.gz) are not separately stored and do not need cleanup.",
			kind: "delete", path: "/releases/{release_id}", pathParams: []string{"release_id"}, expect204: true, write: true,
			schema: objectSchema(map[string]any{"release_id": intProp("The release ID to delete (required).")}, []string{"release_id"}),
		},

		// ---- questionnaire tools ----
		{name: "ask_question", description: "Create a questionnaire in the current issue and wait for users to answer asynchronously. Returns immediately with status='scheduled'; you will be woken with a notification once an answer arrives. End your turn after calling — do not chain other tool calls in the same batch. Use check_questionnaire to poll results, close_questionnaire to close. The questionnaire is single-fill — the first response locks it. Prefer single_choice / multi_choice question types; reserve text_input for genuinely open answers (URLs, names, free-form descriptions). Keep each question short and unambiguous (≤300 chars). When you have a preferred answer, surface it — mark it in the option label (e.g. 'yes (recommended)') or note it in the question text — so the user can pick the suggested choice with minimum friction.",
			kind: "questionnaire", write: true,
			schema: askQuestionSchema(),
		},
		{name: "check_questionnaire", description: "Check the current status and results of a questionnaire by its ID. Returns aggregated tallies for choice questions and text responses for text-input questions. Use this to poll results after being woken by a questionnaire.answered notification.",
			kind: "get", path: "/issues/current/questionnaires/{id}/results", pathParams: []string{"id"}, write: true,
			schema: objectSchema(map[string]any{"id": intProp("The questionnaire id to check results for (required).")}, []string{"id"}),
		},
		{name: "close_questionnaire", description: "Close a questionnaire by its ID. A closed questionnaire no longer accepts answers. Optionally provide a reason. The response includes the final aggregated results.",
			kind: "questionnaire_close", path: "/issues/current/questionnaires/{id}/close", pathParams: []string{"id"}, write: true,
			schema: objectSchema(map[string]any{
				"id":     intProp("The questionnaire id to close (required)."),
				"reason": stringProp("Optional reason for closing the questionnaire."),
			}, []string{"id"}),
		},
	}

	out := make([]local.Tool, 0, len(defs))
	for _, d := range defs {
		// In read-only mode every mutating platform tool is hidden — it
		// must not appear in the returned slice (and thus not in the LLM's
		// tool schema). Read tools are always included.
		if readOnly && d.write {
			continue
		}
		schema := d.schema
		if schema == nil {
			schema = objectSchema(nil, nil)
		}

		switch d.kind {
		case "attachment":
			out = append(out, &attachmentTool{
				name:        d.name,
				description: d.description,
				schema:      schema,
				client:      client,
			})
		case "todo":
			out = append(out, &todoUpdateTool{
				name:        d.name,
				description: d.description,
				schema:      schema,
				client:      client,
			})
		case "create_issue":
			out = append(out, &createIssueTool{
				name:        d.name,
				description: d.description,
				schema:      schema,
				client:      client,
			})
		case "questionnaire":
			out = append(out, &questionnaireTool{
				name:        d.name,
				description: d.description,
				schema:      schema,
				client:      client,
				async:       async,
			})
		case "questionnaire_close":
			// close_questionnaire sends the reason in the JSON body
			// while the questionnaire id is in the path. The standard
			// Tool's Call() sends raw args as the body, so we use a
			// standard Tool with a POST method.
			t := &Tool{
				name:        d.name,
				description: d.description,
				schema:      schema,
				client:      client,
				method:      http.MethodPost,
				buildPath:   paramPath(d.path, d.pathParams...),
			}
			out = append(out, t)
		default:
			method := http.MethodPost
			switch d.kind {
			case "get":
				method = http.MethodGet
			case "patch":
				method = http.MethodPatch
			case "delete":
				method = http.MethodDelete
			}
			t := &Tool{
				name:        d.name,
				description: d.description,
				schema:      schema,
				client:      client,
				method:      method,
				expect204:   d.expect204,
			}
			if len(d.pathParams) > 0 {
				t.buildPath = paramPath(d.path, d.pathParams...)
			} else {
				t.buildPath = staticPath(d.path)
			}
			if len(d.queryParams) > 0 {
				t.buildQuery = queryBuilder(d.queryParams...)
			}
			out = append(out, t)
		}
	}
	return out
}

func objectSchema(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object"}
	if props != nil {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// stringPropMax is stringProp + a maxLength hint. The server is still
// the source of truth; this only helps the LLM avoid the round-trip.
func stringPropMax(desc string, maxLen int) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": desc,
		"maxLength":   maxLen,
	}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func enumProp(desc string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func issueEditSchema() map[string]any {
	return map[string]any{
		"type":          "object",
		"minProperties": 1,
		"properties": map[string]any{
			"title": map[string]any{
				"type":        "string",
				"description": "New title for the current issue. Must be non-empty and ≤200 characters. Omit to leave unchanged.",
				"minLength":   1,
				"maxLength":   200,
			},
			"body": map[string]any{
				"type":        "string",
				"description": "New body (markdown) for the current issue. Omit to leave unchanged.",
			},
		},
	}
}

func askQuestionSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{"title", "questions"},
		"properties": map[string]any{
			"title": map[string]any{
				"type":        "string",
				"description": "Questionnaire title. 1-200 characters.",
				"minLength":   1,
				"maxLength":   200,
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional longer description for the questionnaire (markdown). Up to 2000 characters.",
				"maxLength":   2000,
			},
			"questions": map[string]any{
				"type":        "array",
				"description": "Questions to include. 1-20 items.",
				"minItems":    1,
				"maxItems":    20,
				"items": map[string]any{
					"type": "object",
					"required": []string{"type", "text"},
					"properties": map[string]any{
						"type": map[string]any{
							"type":        "string",
							"enum":        []string{"single_choice", "multi_choice", "text_input"},
							"description": "Question type. Prefer single_choice or multi_choice for bounded, predictable answer spaces. Use text_input only when the answer cannot reasonably be anticipated.",
						},
						"text": map[string]any{
							"type":        "string",
							"description": "Question text. 1-300 characters. Keep it concise and unambiguous.",
							"minLength":   1,
							"maxLength":   300,
						},
						"required": map[string]any{
							"type":        "boolean",
							"description": "Whether an answer is required. Default true.",
							"default":     true,
						},
						"options": map[string]any{
							"type":        "array",
							"description": "Answer options for choice types (single_choice, multi_choice). 2-10 items. Not used for text_input.",
							"minItems":    2,
							"maxItems":    10,
							"items": map[string]any{
								"type": "object",
								"required": []string{"label"},
								"properties": map[string]any{
									"label": map[string]any{
										"type":        "string",
										"description": "Option label shown to the user. 1-100 characters.",
										"minLength":   1,
										"maxLength":   100,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
