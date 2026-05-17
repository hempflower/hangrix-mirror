// Package handler exposes the platform's agent tools over plain REST.
//
// Each tool the platform implements (issue_read / issue_diff /
// issue_comment / …) is reachable at `POST /api/agent/tools/{name}`.
// Bearer-auth uses the `hgxs_` session token; per-role filtering happens
// against the session's role_config snapshot (whitelist `can:` wins; if
// empty the `not:` blacklist applies; both empty fails closed).
//
// Wire shape (intentionally tiny — agents only need request/response,
// no protocol envelope):
//
//	POST /api/agent/tools/issue_comment
//	Authorization: Bearer hgxs_…
//	Content-Type: application/json
//	{ "body": "hello" }                  // tool-specific args (empty {} ok)
//
//	200 OK
//	{ "is_error": false, "text": "{\"id\":42,…}" }
//
// `text` is the same payload an MCP `content[0].text` part would carry;
// the agent surfaces it verbatim to the LLM as the function-call output.
// Soft failures (ACL denial, unknown tool, validation errors inside the
// tool) come back as 200 + is_error=true so the LLM can self-correct;
// hard failures (missing/invalid token, malformed request) get 4xx with
// `{ "error": "…" }`.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	platformmcpdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/service"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// callTimeout caps how long a single tool invocation may take. Each
// tool's own subsystem may have shorter internal timeouts; this is the
// outer envelope so a stuck tool doesn't pin a session thread.
const callTimeout = 60 * time.Second

// maxRequestBody bounds the JSON arg payload. The biggest expected case
// is a `issue_comment` with a long body; 1 MiB is generous.
const maxRequestBody = 1 << 20

// Registry is the subset of *service.Registry the handler needs.
// Defined as an interface so tests can substitute a fake without
// instantiating the whole tool catalogue (with its issue / runner / git
// deps).
type Registry interface {
	ByName(name string) *platformmcpdomain.Tool
	FilterForSession(sess *runnerdomain.AgentSession) []*platformmcpdomain.Tool
}

type Handler struct {
	registry  Registry
	validator runnerdomain.SessionTokenValidator
}

type HandlerDeps struct {
	Registry  *service.Registry
	Validator runnerdomain.SessionTokenValidator
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		registry:  deps.Registry,
		validator: deps.Validator,
	}
}

// NewHandlerWithRegistry is the test entry point: supply a fake
// Registry that returns whatever tools the test cares about.
func NewHandlerWithRegistry(registry Registry, validator runnerdomain.SessionTokenValidator) *Handler {
	return &Handler{registry: registry, validator: validator}
}

type ctxKey int

const ctxKeySession ctxKey = iota

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/agent/tools", func(r chi.Router) {
		r.Use(h.bearerAuth)
		r.Get("/", h.list)
		r.Post("/{name}", h.call)
	})
}

// bearerAuth resolves Authorization: Bearer hgxs_... → AgentSession.
// 401 on missing/malformed header; 403 on token invalid/inactive.
func (h *Handler) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(raw, prefix) {
			httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
		if token == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		sess, err := h.validator.ValidateSessionToken(r.Context(), token)
		if err != nil {
			switch {
			case errors.Is(err, runnerdomain.ErrInvalidSessionToken):
				httpx.WriteError(w, http.StatusForbidden, "invalid session token")
			case errors.Is(err, runnerdomain.ErrSessionTokenInactive):
				httpx.WriteError(w, http.StatusForbidden, "session token revoked or session terminated")
			default:
				httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeySession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// toolDescriptor is the JSON projection of one tool surfaced by GET
// /api/agent/tools. Mirrors the MCP `tools/list` shape so an agent that
// wants to verify its built-in schemas against the live server can
// diff them.
type toolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(ctxKeySession).(*runnerdomain.AgentSession)
	if sess == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	tools := h.registry.FilterForSession(sess)
	out := make([]toolDescriptor, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolDescriptor{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tools": out})
}

// callResponse is the success envelope every tool returns. Soft failures
// (ACL denied, unknown tool, validation errors) come back as 200 with
// is_error=true; hard failures are 4xx + httpx error JSON.
type callResponse struct {
	IsError bool   `json:"is_error"`
	Text    string `json:"text"`
}

func (h *Handler) call(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(ctxKeySession).(*runnerdomain.AgentSession)
	if sess == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "tool name required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if int64(len(body)) > maxRequestBody {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	// Empty body and whitespace-only body both map to "no args". The tool
	// impls themselves accept `{}` for the no-arg case (see unmarshalArgs
	// in service/tools_write.go).
	args := json.RawMessage(body)
	if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage(`{}`)
	}

	if !service.CanCallTool(sess, name) {
		httpx.WriteJSON(w, http.StatusOK, callResponse{
			IsError: true,
			Text:    fmt.Sprintf("tool %q is not granted to role %q (host yaml `can:` / `not:` ACL)", name, sess.RoleKey),
		})
		return
	}
	tool := h.registry.ByName(name)
	if tool == nil {
		httpx.WriteJSON(w, http.StatusOK, callResponse{
			IsError: true,
			Text:    fmt.Sprintf("unknown tool %q", name),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
	defer cancel()
	res, err := tool.Call(ctx, sess, args)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, callResponse{
		IsError: res.IsError,
		Text:    res.Text,
	})
}
