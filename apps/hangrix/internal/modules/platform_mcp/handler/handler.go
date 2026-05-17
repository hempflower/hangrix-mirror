// Package handler implements the M7b platform MCP server. One endpoint
// at /api/mcp/v1 speaks JSON-RPC 2.0 over single-shot POST replies
// (no SSE, no notifications — see docs/runner-protocol.md). Bearer auth
// uses the `hgxs_` session token; per-role filtering happens against the
// session's role_config snapshot.
//
// Only `tools/list` and `tools/call` are implemented. `initialize` is
// served as a passthrough no-op so the agent's MCP client doesn't have
// to special-case our server in its handshake.
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

// maxRequestBody bounds the JSON-RPC envelope. The biggest expected
// payload is a `tools/call` with a large argument blob (e.g. a long
// comment body); 1 MiB is generous.
const maxRequestBody = 1 << 20

// Registry is the subset of *service.Registry the handler needs. Defined
// as an interface so tests can substitute a fake without instantiating
// the whole tool catalogue (with its issue / runner / git deps).
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
	r.Route("/api/mcp/v1", func(r chi.Router) {
		r.Use(h.bearerAuth)
		r.Post("/", h.dispatch)
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

// rpcEnvelope is the JSON-RPC 2.0 request shape. `id` may be a number,
// string, or null — JSON-RPC clients reuse the same wire type for
// notifications (no id) and we want to echo back exactly what we got.
type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// JSON-RPC error codes per spec. We only need a small subset.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(ctxKeySession).(*runnerdomain.AgentSession)
	if sess == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		writeRPCError(w, nil, codeParseError, "read body: "+err.Error())
		return
	}
	if int64(len(body)) > maxRequestBody {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	var env rpcEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		writeRPCError(w, nil, codeParseError, "invalid JSON: "+err.Error())
		return
	}
	if env.JSONRPC != "2.0" {
		writeRPCError(w, env.ID, codeInvalidRequest, "jsonrpc must be 2.0")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
	defer cancel()
	switch env.Method {
	case "initialize":
		// Minimal initialize handshake. Echo `protocolVersion` if the
		// client sent one; advertise tools capability.
		writeRPCResult(w, env.ID, map[string]any{
			"protocolVersion": "2025-03-26",
			"serverInfo": map[string]any{
				"name":    "hangrix-platform-mcp",
				"version": "0.1",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
		})
	case "tools/list":
		tools := h.registry.FilterForSession(sess)
		out := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			out = append(out, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		writeRPCResult(w, env.ID, map[string]any{"tools": out})
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(env.Params, &params); err != nil {
			writeRPCError(w, env.ID, codeInvalidParams, "params invalid: "+err.Error())
			return
		}
		if params.Name == "" {
			writeRPCError(w, env.ID, codeInvalidParams, "tool name required")
			return
		}
		if !service.CanCallTool(sess, params.Name) {
			writeRPCResult(w, env.ID, errorContent(fmt.Sprintf(
				"tool %q is not granted to role %q (host yaml `can:` list)", params.Name, sess.RoleKey,
			)))
			return
		}
		tool := h.registry.ByName(params.Name)
		if tool == nil {
			writeRPCResult(w, env.ID, errorContent(fmt.Sprintf("unknown tool %q", params.Name)))
			return
		}
		res, err := tool.Call(ctx, sess, params.Arguments)
		if err != nil {
			writeRPCError(w, env.ID, codeInternalError, err.Error())
			return
		}
		writeRPCResult(w, env.ID, map[string]any{
			"isError": res.IsError,
			"content": []any{
				map[string]any{"type": "text", "text": res.Text},
			},
		})
	case "notifications/initialized", "notifications/cancelled":
		// JSON-RPC notifications carry no id and expect no response. We
		// still send a 200 with no body — agents that send `id` for
		// these get a benign empty result, which is harmless.
		w.WriteHeader(http.StatusOK)
	default:
		writeRPCError(w, env.ID, codeMethodNotFound, "method not found: "+env.Method)
	}
}

// errorContent builds a structured isError result with one text part.
// Used for soft-fail outcomes (tool name unknown, permission denied)
// where we want the LLM to see the error inline rather than the agent
// to crash.
func errorContent(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []any{
			map[string]any{"type": "text", "text": msg},
		},
	}
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

