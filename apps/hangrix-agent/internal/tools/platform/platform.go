// Package platform implements the agent-side "platform tools" as
// built-ins. Each tool is a thin HTTP shim that POSTs the LLM-emitted
// argument JSON to the platform's REST endpoint and returns whatever
// the server gives back.
//
// We used to discover these over an MCP `tools/list` round-trip; that
// extra protocol layer is gone. The agent now ships hardcoded
// descriptors (name + description + JSON-Schema) that must match the
// server's service-layer definitions. A drift between the two surfaces
// as a 400 / 404 on the first call — not silent.
//
// Wire shape (one endpoint per tool, see
// apps/hangrix/internal/modules/agent_api/handler/handler.go):
//
//	POST <base>/<tool-name>
//	Authorization: Bearer hgxs_…
//	Content-Type: application/json
//	<args JSON>
//
//	200 { "is_error": false, "text": "<server-side payload>" }
//
// Soft errors (ACL denied, validation failure inside the tool) ride on
// is_error=true with the explanation in `text`; the agent surfaces both
// to the LLM verbatim.
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
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		// httpx.NewClient honours HANGRIX_INSECURE_SKIP_TLS_VERIFY
		// so the platform-tools path uses the same TLS knob as the
		// LLM client when the container's CA store is broken.
		http: httpx.NewClient(60 * time.Second),
	}
}

// callResponse mirrors the server's callResponse JSON shape. is_error
// + text are the only fields; success vs soft-error is the only
// distinction the agent needs to surface.
type callResponse struct {
	IsError bool   `json:"is_error"`
	Text    string `json:"text"`
}

// Call POSTs args to <base>/<name>. The returned string is what the
// LLM sees as the function-call output; isError is forwarded so the
// runtime can flag the tool_call frame appropriately.
//
// Retry policy mirrors the LLM client: same 3-attempt exponential
// backoff for transport errors and 5xx. 4xx is terminal — schema
// mismatch / auth fail don't get better on retry.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error) {
	if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage(`{}`)
	}
	url := c.baseURL + "/" + name

	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(math.Pow(2, float64(attempt-1))*500) * time.Millisecond
			select {
			case <-ctx.Done():
				return "", false, ctx.Err()
			case <-time.After(delay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(args))
		if err != nil {
			return "", false, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return "", false, ctx.Err()
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("platform tool %q: http %d: %s", name, resp.StatusCode, truncate(body, 256))
			continue
		}
		if resp.StatusCode >= 400 {
			// 4xx is terminal — the request itself is wrong (auth fail,
			// malformed body). Surface verbatim; no retry will help.
			return "", false, fmt.Errorf("platform tool %q: http %d: %s", name, resp.StatusCode, truncate(body, 512))
		}
		var out callResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return "", false, fmt.Errorf("platform tool %q: decode response: %w", name, err)
		}
		return out.Text, out.IsError, nil
	}
	if lastErr == nil {
		lastErr = errors.New("platform tool: exhausted retries")
	}
	return "", false, lastErr
}

// PostMultipart POSTs a raw body (with the given Content-Type) to
// <base>/<name>. Used for multipart file uploads where the body is not
// JSON. The body is small enough to buffer in memory (max 64 MiB + form
// overhead) so retries are cheap — we re-create the reader from the
// byte slice each attempt.
//
// Retry policy mirrors Call: 3-attempt exponential backoff on transport
// errors and 5xx. 4xx is terminal.
func (c *Client) PostMultipart(ctx context.Context, name string, body []byte, contentType string) (text string, isError bool, err error) {
	url := c.baseURL + "/" + name

	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(math.Pow(2, float64(attempt-1))*500) * time.Millisecond
			select {
			case <-ctx.Done():
				return "", false, ctx.Err()
			case <-time.After(delay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return "", false, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return "", false, ctx.Err()
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
			lastErr = fmt.Errorf("platform tool %q: http %d: %s", name, resp.StatusCode, truncate(respBody, 256))
			continue
		}
		if resp.StatusCode >= 400 {
			return "", false, fmt.Errorf("platform tool %q: http %d: %s", name, resp.StatusCode, truncate(respBody, 512))
		}
		var out callResponse
		if err := json.Unmarshal(respBody, &out); err != nil {
			return "", false, fmt.Errorf("platform tool %q: decode response: %w", name, err)
		}
		return out.Text, out.IsError, nil
	}
	if lastErr == nil {
		lastErr = errors.New("platform tool: exhausted retries")
	}
	return "", false, lastErr
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// Tool wraps a single hardcoded platform tool descriptor (the
// name/description/schema we used to fetch over MCP) as a
// local.Tool. The Call method is the HTTP shim above.
type Tool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client
}

func (t *Tool) Name() string             { return t.name }
func (t *Tool) Description() string      { return t.description }
func (t *Tool) Schema() map[string]any   { return t.schema }

// Call returns the platform's text payload as the result. Soft errors
// (is_error=true) are returned as a structured `{is_error, text}`
// object so the registry can mark the IPC tool_call frame accordingly
// and the LLM still sees the failure reason.
//
// We deliberately do NOT use a Go error for soft failures here — those
// represent "the tool ran, the server said no" rather than "the call
// itself blew up", and the LLM benefits from seeing the response text
// verbatim. Transport / 5xx / 4xx errors collapse to Go errors.
func (t *Tool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	text, isError, err := t.client.Call(ctx, t.name, args)
	if err != nil {
		return nil, err
	}
	if isError {
		return map[string]any{"is_error": true, "text": text}, nil
	}
	// Most tools serialize their result as a JSON string in `text`; if
	// it parses, forward the structured value so the LLM sees nice
	// nested JSON rather than an escaped string. Plain-text payloads
	// (rare — issue_close returns one) fall through as a string.
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed, nil
	}
	return text, nil
}

// attachmentTool is like Tool but reads the file from the local workspace
// and sends it as multipart/form-data (real file upload — no base64
// encoding) to the server. The server-side handler receives the file
// bytes as a "file" form part plus metadata fields.
//
// Paths are resolved against the workspace root (/workspace) with
// symlink-target containment — any path that resolves outside the
// workspace (including via symlink) is rejected to prevent secret
// exfiltration.
//
// The LLM-facing descriptor is identical to a plain Tool — the agent
// only knows it passes a "path" and gets back attachment metadata.
type attachmentTool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client
}

func (t *attachmentTool) Name() string           { return t.name }
func (t *attachmentTool) Description() string    { return t.description }
func (t *attachmentTool) Schema() map[string]any { return t.schema }

// maxAttachmentBytes is the maximum file size the agent will read and
// upload (64 MiB, matching the server-side limit).
const maxAttachmentBytes = 64 << 20

// workspaceRoot is the agent container's working tree mount point.
const workspaceRoot = "/workspace"

// resolveWorkspacePath resolves a user-supplied path (relative or
// absolute) to a real, symlink-resolved absolute path that MUST fall
// within workspaceRoot.  Returns an error if the path is outside the
// workspace, is a symlink that escapes, or cannot be resolved.
func resolveWorkspacePath(p string) (string, error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(workspaceRoot, p)
	}

	// Ensure the original path (before symlink resolution) is within workspace.
	if !strings.HasPrefix(p, workspaceRoot+"/") && p != workspaceRoot {
		return "", fmt.Errorf("path %q is outside workspace", p)
	}
	// Resolve symlinks to get the real on-disk target.
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", p, err)
	}
	// Containment: the resolved path must be inside workspaceRoot.
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

	// Build multipart/form-data body with the file bytes and metadata.
	// This is a real binary file upload — no base64 overhead.
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

	text, isError, err := t.client.PostMultipart(ctx, t.name, body.Bytes(), w.FormDataContentType())
	if err != nil {
		return nil, err
	}
	if isError {
		return map[string]any{"is_error": true, "text": text}, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed, nil
	}
	return text, nil
}

// maxPatchSeriesBytes bounds the combined size of every .patch file in a
// single issue_patch_submit. The patch text travels inside the JSON
// request body, so this must stay within the server's per-call body
// budget for the patch-submit route (see maxPatchSubmitBody in the
// platform handler).
const maxPatchSeriesBytes = 16 << 20

// patchSubmitTool reads the .patch files named in `patch_paths` from the
// local workspace and submits their *contents* to the server, exactly as
// attachmentTool does for uploads. The server is a separate process with
// no access to the agent's working tree — handing it bare workspace paths
// (the old behaviour) made it read from its own canonical repo storage,
// where the agent's git-format-patch output does not exist. Reading the
// files agent-side and shipping the bytes is the only correct split.
//
// The LLM-facing descriptor still takes `patch_paths`; the path→content
// transform is invisible to the model.
type patchSubmitTool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client
}

func (t *patchSubmitTool) Name() string           { return t.name }
func (t *patchSubmitTool) Description() string    { return t.description }
func (t *patchSubmitTool) Schema() map[string]any { return t.schema }

func (t *patchSubmitTool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	var req struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		BaseHeadSHA string   `json:"base_head_sha"`
		PatchPaths  []string `json:"patch_paths"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("issue_patch_submit: parse arguments: %w. Pass a JSON object with title, description, base_head_sha, and patch_paths.", err)
	}
	if len(req.PatchPaths) == 0 {
		return nil, fmt.Errorf("issue_patch_submit: patch_paths is required and must list at least one .patch file (generate them with `git format-patch`).")
	}

	type patchFile struct {
		FileName  string `json:"file_name"`
		PatchText string `json:"patch_text"`
	}
	patches := make([]patchFile, 0, len(req.PatchPaths))
	var total int
	for i, p := range req.PatchPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("issue_patch_submit: patch_paths[%d] is empty.", i)
		}
		safePath, err := resolveWorkspacePath(p)
		if err != nil {
			return nil, fmt.Errorf("issue_patch_submit: %w. Only files inside the workspace can be submitted.", err)
		}
		data, err := os.ReadFile(safePath)
		if err != nil {
			return nil, fmt.Errorf("issue_patch_submit: cannot read patch_paths[%d] %q: %w. Check that the path exists in the workspace.", i, p, err)
		}
		total += len(data)
		if total > maxPatchSeriesBytes {
			return nil, fmt.Errorf("issue_patch_submit: patch series exceeds the %d MiB limit. Split the work into a smaller series.", maxPatchSeriesBytes>>20)
		}
		// Preserve the workspace-relative path as the file name so the
		// server-side display (file_name / source_path) is unchanged.
		patches = append(patches, patchFile{FileName: p, PatchText: string(data)})
	}

	payload, err := json.Marshal(map[string]any{
		"title":         req.Title,
		"description":   req.Description,
		"base_head_sha": req.BaseHeadSHA,
		"patches":       patches,
	})
	if err != nil {
		return nil, fmt.Errorf("issue_patch_submit: encode request: %w", err)
	}

	text, isError, err := t.client.Call(ctx, t.name, payload)
	if err != nil {
		return nil, err
	}
	if isError {
		return map[string]any{"is_error": true, "text": text}, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed, nil
	}
	return text, nil
}

// All returns every platform tool the agent ships with, bound to the
// supplied HTTP client. Order matters for catalogue stability — keep
// read-only tools first then mutating ones, mirroring the order the
// MCP server used to declare them.
//
// Schemas duplicate apps/hangrix/internal/modules/agent_api/service/
// tools_{read,write}.go. The duplication is bounded (one block per
// tool, ~10 lines each) and the cost — drift surfaces as a 4xx on
// the first real call — is fine for v1. A future "fetch catalogue at
// startup" path could collapse this, but the built-in approach keeps
// the agent boot deterministic and runnable offline (smoke tests).
func All(client *Client) []local.Tool {
	if client == nil {
		return nil
	}
	descriptors := []struct {
		name, description string
		schema            map[string]any
	}{
		{
			name:        "issue_read",
			description: "Read the current issue's metadata, comments, and timeline events.",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_diff",
			description: "Return the diff between the issue branch and its base branch (file-level unified diff).",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_mergeable",
			description: "Check whether the issue branch can be merged into its base — tries fast-forward first, then checks whether auto-rebase would succeed. mergeable=true means issue_merge is expected to succeed. Returns mergeable, mode, base_branch, base_sha, head_sha, and hint.",
			schema:      objectSchema(nil, nil),
		},

		{
			name:        "issue_children",
			description: "List sub-issues (child issues) of the current issue.",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_read_by_number",
			description: "Read an issue by its number (e.g. 91). Returns the issue's metadata, comments, and timeline events. Only works for issues within the same repository as the current session.",
			schema: objectSchema(map[string]any{
				"issue_number": intProp("The issue number to read (required, e.g. 91). Must belong to the same repository as the current session."),
			}, []string{"issue_number"}),
		},
		{
			name:        "issue_checks",
			description: "List the latest state of each CI check on the issue's head commit. Currently always returns [].",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "roster_list",
			description: "List every active role session on the current issue. Each item includes a `last_activity_at` field showing the most recent activity timestamp for that session — use it to detect stalled agents.",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_create",
			description: "Create a new issue in the current repo. Set `parent: true` to create as a sub-issue of the current issue.",
			schema: objectSchema(map[string]any{
				"title":  stringProp("Issue title (1-200 characters)."),
				"body":   stringProp("Optional issue body (markdown)."),
				"parent": boolProp("When true, creates the new issue as a sub-issue of the current issue. Default: false."),
			}, []string{"title"}),
		},
		{
			name:        "issue_comment",
			description: "Post a comment on the current issue. `body` is markdown; @agent-<role-key> mentions wake other roles.",
			schema: objectSchema(map[string]any{
				"body":      stringProp("The comment body. Markdown allowed; mentions follow @agent-<role-key> grammar."),
				"file_path": stringProp("Optional path to anchor the comment to a file (inline review). Omit for top-level."),
				"line":      intProp("Optional line number to anchor inline. Requires file_path."),
			}, []string{"body"}),
		},
		{
			name:        "issue_review_vote",
			description: "Cast a structured review vote on the current issue (approve / request_changes / abstain).",
			schema: objectSchema(map[string]any{
				"value": enumProp("Vote outcome.", []string{"approve", "request_changes", "abstain"}),
				"reason": stringProp("Free-text rationale shown on the timeline. Recommended even for 'approve'."),
			}, []string{"value"}),
		},
		{
			name:        "issue_close",
			description: "Close the current issue without merging. Archives every active agent session on it.",
			schema: objectSchema(map[string]any{
				"reason": stringProp("Optional rationale, recorded on the timeline."),
			}, nil),
		},
		{
			name:        "issue_merge",
			description: "Merge the issue branch into its base — tries fast-forward first, falls back to auto-rebase. Fails if there are no commits or a rebase conflict.",
			schema: objectSchema(map[string]any{
				"message": stringProp("Optional merge message."),
			}, nil),
		},
		{
			name:        "session_recover",
			description: "Recover a failed / succeeded / cancelled / idle session on the current issue. Sets it back to pending so the runner picks it up. Restricted to sessions on the same issue.",
			schema: objectSchema(map[string]any{
				"session_id": intProp("The session ID to recover. Must be on the same issue as the caller."),
			}, []string{"session_id"}),
		},
		{
			name:        "issue_attachment_upload",
			description: "Upload a file from the workspace as an issue attachment. Returns attachment metadata including an `attachment_id` and `markdown_snippet` — use `issue_comment` to insert the snippet into a comment body. `path` must be a workspace-relative or absolute path to an existing file. Set `inline` to true for images/videos you want rendered inline (produces `![attachment:N]` syntax); false / omitted produces `[attachment:N]` link syntax.",
			schema: objectSchema(map[string]any{
				"path":         stringProp("Workspace-relative or absolute path to the file to upload. Required."),
				"display_name": stringProp("Optional display name for the attachment. Defaults to the file's basename."),
				"inline":       boolProp("When true, produces inline syntax `![attachment:N]` for images/videos. Default false."),
				"comment_id":   intProp("Optional comment ID to bind the attachment to an existing comment."),
			}, []string{"path"}),
		},

		{
			name:        "issue_patch_submit",
			description: "Submit a code contribution as a patch series against the issue branch. Use this instead of git push. First run `git format-patch <base_branch>` to generate one or more .patch files, then pass their workspace paths in `patch_paths`. The files are read in order and preserved as a patch series. The maintainer will review and apply the patch.",
			schema: objectSchema(map[string]any{
				"title":         stringProp("Short, descriptive title for the patch (required)."),
				"description":   stringProp("Description of what was changed and why (required)."),
				"base_head_sha": stringProp("The issue branch's head commit SHA at the time work started (required)."),
				"patch_paths":   stringArrayProp("Workspace-relative paths to .patch files generated by `git format-patch`. Files are read in this order and preserved as a patch series (required)."),
			}, []string{"title", "description", "base_head_sha", "patch_paths"}),
		},

		{
			name:        "issue_patch_list",
			description: "List all patch submissions on the current issue. Returns submission-level summaries with status and patch_count.",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_patch_read",
			description: "Read a single patch submission by ID. Returns submission metadata plus an ordered list of patch files (patches[]), each with index, file_name, source_path, subject (optional), and patch_text. The patches array is ordered — apply in sequence with git am.",
			schema: objectSchema(map[string]any{
				"id": intProp("The patch submission ID to read (required)."),
			}, []string{"id"}),
		},

		{
			name:        "issue_patch_apply",
			description: "Request the apply agent to apply a patch submission to the issue branch. Only submissions with status='submitted' can be requested. The application happens asynchronously: the server sets status to 'applying' and dispatches a patch.apply_requested event; the apply agent performs git am + git push in its workspace and reports back via issue_patch_apply_result. Returns 'accepted' confirmation — do not expect a commit_sha in the response. Requires `issue_patch_apply` in the role's `can:` whitelist.",
			schema: objectSchema(map[string]any{
				"id": intProp("The patch submission ID to request application for (required)."),
			}, []string{"id"}),
		},
		{
			name:        "issue_patch_reject",
			description: "Reject a patch submission. Only submissions with status='submitted' or 'applying' can be rejected. Records a rejection reason on the timeline. Requires `issue_patch_reject` in the role's `can:` whitelist.",
			schema: objectSchema(map[string]any{
				"id":     intProp("The patch submission ID to reject (required)."),
				"reason": stringProp("Reason for rejection (required, shown on timeline)."),
			}, []string{"id", "reason"}),
		},
		{
			name:        "issue_patch_withdraw",
			description: "Withdraw your own patch submission. Only the original submitter (same role on the same issue) can withdraw. Only submissions with status='submitted' can be withdrawn; terminal statuses (applied/rejected/superseded/voided) and in-flight statuses (applying) cannot. The submission is marked 'withdrawn' and a timeline event is recorded.",
			schema: objectSchema(map[string]any{
				"id": intProp("The patch submission ID to withdraw (required)."),
			}, []string{"id"}),
		},

		{
			name:        "issue_patch_apply_result",
			description: "Report the result of applying a patch submission via git am + git push in the workspace. Call this after the git am workflow completes (success or failure). On success, pass the new commit_sha — the server marks the submission 'applied' and updates the issue head. On failure, pass an error description distinguishing conflict, am-failure, or push-failure — the server records the error on the submission (status stays 'applying' so it can be retried).",
			schema: objectSchema(map[string]any{
				"submission_id": intProp("The patch submission ID that was applied (required)."),
				"success":       boolProp("Whether git am + git push succeeded (required)."),
				"commit_sha":    stringProp("The new commit SHA on the issue branch after successful apply. Required when success=true."),
				"error":         stringProp("Error description when success=false. Distinguish: conflict, am-failure, push-failure."),
			}, []string{"submission_id", "success"}),
		},

	{
		name:        "release_create",
		description: "Create a new release in draft state from an existing git tag. The tag must already exist in the repo.",
		schema: objectSchema(map[string]any{
			"tag_name": stringProp("The existing git tag to create the release from (required)."),
			"title":    stringProp("Optional release title. Defaults to the tag name if omitted."),
			"notes":    stringProp("Optional release notes (markdown)."),
		}, []string{"tag_name"}),
	},
	{
		name:        "release_upload_asset",
		description: "Upload a custom asset to a release. The file content must be base64-encoded.",
		schema: objectSchema(map[string]any{
			"release_id":   intProp("The release ID to attach the asset to (required)."),
			"name":         stringProp("Asset file name (required)."),
			"content":      stringProp("Base64-encoded file content (required)."),
			"content_type": stringProp("Optional MIME type. Defaults to application/octet-stream."),
		}, []string{"release_id", "name", "content"}),
	},
	{
		name:        "release_publish",
		description: "Publish a draft release, making it visible as an official release with a published_at timestamp.",
		schema: objectSchema(map[string]any{
			"release_id": intProp("The release ID to publish (required)."),
		}, []string{"release_id"}),
	},
	{
		name:        "release_update",
		description: "Edit an existing release's metadata (title, notes). The tag_name can only be changed while the release is still a draft.",
		schema: objectSchema(map[string]any{
			"release_id": intProp("The release ID to update (required)."),
			"title":      stringProp("Optional new release title."),
			"notes":      stringProp("Optional new release notes (markdown)."),
			"tag_name":   stringProp("Optional new tag name. Only mutable when the release is still a draft."),
		}, []string{"release_id"}),
	},
	{
		name:        "release_delete",
		description: "Delete a release and all of its custom assets. Derived source archives (zip/tar.gz) are not separately stored and do not need cleanup.",
		schema: objectSchema(map[string]any{
			"release_id": intProp("The release ID to delete (required)."),
		}, []string{"release_id"}),
	},
	}
	out := make([]local.Tool, 0, len(descriptors))
	for _, d := range descriptors {
		switch d.name {
		case "issue_attachment_upload":
			out = append(out, &attachmentTool{
				name:        d.name,
				description: d.description,
				schema:      d.schema,
				client:      client,
			})
		case "issue_patch_submit":
			out = append(out, &patchSubmitTool{
				name:        d.name,
				description: d.description,
				schema:      d.schema,
				client:      client,
			})
		default:
			out = append(out, &Tool{
				name:        d.name,
				description: d.description,
				schema:      d.schema,
				client:      client,
			})
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

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func enumProp(desc string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func stringArrayProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
