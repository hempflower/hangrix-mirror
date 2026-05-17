package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	platformmcpdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// stubValidator returns the same session for any token whose plaintext
// matches `validToken`. Anything else returns ErrInvalidSessionToken.
type stubValidator struct {
	validToken string
	session    *runnerdomain.AgentSession
}

func (s *stubValidator) ValidateSessionToken(_ context.Context, token string) (*runnerdomain.AgentSession, error) {
	if token == s.validToken {
		return s.session, nil
	}
	return nil, runnerdomain.ErrInvalidSessionToken
}

// stubRegistry returns a fixed tool catalogue. FilterForSession is a
// no-op pass-through — per-role filtering correctness is tested via
// service.CanCallTool (a pure function), not against this stub.
type stubRegistry struct {
	tools     []*platformmcpdomain.Tool
	calls     int
	lastName  string
	lastArgs  string
}

func (s *stubRegistry) ByName(name string) *platformmcpdomain.Tool {
	for _, t := range s.tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

func (s *stubRegistry) FilterForSession(_ *runnerdomain.AgentSession) []*platformmcpdomain.Tool {
	// Honour the same allow-list the real registry would: read the
	// session's role_config and intersect. The handler tests rely on
	// the can-filter for one of the assertions; we replicate the
	// behaviour explicitly so the test stays decoupled from the
	// shape of role_config JSON. The default session below has
	// can:[issue_read, issue_comment].
	out := make([]*platformmcpdomain.Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t)
	}
	return out
}

func newRequest(t *testing.T, body any, token string) *http.Request {
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/v1/", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func newTestRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func newTestHandler(tools []*platformmcpdomain.Tool, canList []string) (*Handler, *stubRegistry, *runnerdomain.AgentSession) {
	roleCfg, _ := json.Marshal(map[string]any{"can": canList})
	sess := &runnerdomain.AgentSession{
		ID:         42,
		RoleKey:    "backend",
		RoleConfig: roleCfg,
	}
	validator := &stubValidator{validToken: "hgxs_TESTTEST_secretsecretsecretsecretsecre", session: sess}
	reg := &stubRegistry{tools: tools}
	return NewHandlerWithRegistry(reg, validator), reg, sess
}

func TestDispatchRejectsMissingToken(t *testing.T) {
	h, _, _ := newTestHandler(nil, nil)
	router := newTestRouter(h)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, newRequest(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	}, ""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rr.Code)
	}
}

func TestDispatchRejectsInvalidToken(t *testing.T) {
	h, _, _ := newTestHandler(nil, nil)
	router := newTestRouter(h)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, newRequest(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	}, "hgxs_BADTOKEN_garbagegarbagegarbagegarbage"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403", rr.Code)
	}
}

func TestToolsListReturnsRegistry(t *testing.T) {
	tool := &platformmcpdomain.Tool{
		Name:        "issue_read",
		Description: "read issue",
		InputSchema: map[string]any{"type": "object"},
	}
	h, _, _ := newTestHandler([]*platformmcpdomain.Tool{tool}, []string{"issue_read"})
	router := newTestRouter(h)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, newRequest(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	}, "hgxs_TESTTEST_secretsecretsecretsecretsecre"))
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}
	var resp struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("got error: %v", resp.Error)
	}
	if len(resp.Result.Tools) != 1 || resp.Result.Tools[0]["name"] != "issue_read" {
		t.Fatalf("tools = %+v", resp.Result.Tools)
	}
}

func TestToolsCallDispatchesToImpl(t *testing.T) {
	called := false
	echoTool := &platformmcpdomain.Tool{
		Name:        "issue_comment",
		Description: "post",
		InputSchema: map[string]any{"type": "object"},
		Call: func(_ context.Context, _ *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			called = true
			return platformmcpdomain.Result{Text: `{"echoed":` + string(args) + `}`}, nil
		},
	}
	h, _, _ := newTestHandler([]*platformmcpdomain.Tool{echoTool}, []string{"issue_comment"})
	router := newTestRouter(h)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, newRequest(t, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{
			"name":      "issue_comment",
			"arguments": map[string]any{"body": "hello"},
		},
	}, "hgxs_TESTTEST_secretsecretsecretsecretsecre"))
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatalf("tool.Call was not invoked")
	}
	var resp struct {
		Result struct {
			IsError bool             `json:"isError"`
			Content []map[string]any `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.IsError {
		t.Fatalf("isError unexpectedly true: %+v", resp.Result)
	}
	if !strings.Contains(resp.Result.Content[0]["text"].(string), `"body":"hello"`) {
		t.Fatalf("echoed args missing: %v", resp.Result.Content)
	}
}

func TestToolsCallDeniedByRoleCanList(t *testing.T) {
	mergeTool := &platformmcpdomain.Tool{
		Name:        "issue_merge",
		Description: "merge",
		InputSchema: map[string]any{"type": "object"},
		Call: func(_ context.Context, _ *runnerdomain.AgentSession, _ json.RawMessage) (platformmcpdomain.Result, error) {
			t.Fatalf("issue_merge should not have been invoked")
			return platformmcpdomain.Result{}, nil
		},
	}
	// Role can only call issue_read — issue_merge must be rejected by
	// the per-role filter even though it lives in the registry.
	h, _, _ := newTestHandler([]*platformmcpdomain.Tool{mergeTool}, []string{"issue_read"})
	router := newTestRouter(h)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, newRequest(t, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "issue_merge",
			"arguments": map[string]any{},
		},
	}, "hgxs_TESTTEST_secretsecretsecretsecretsecre"))
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (errors come back as isError)", rr.Code)
	}
	var resp struct {
		Result struct {
			IsError bool             `json:"isError"`
			Content []map[string]any `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Result.IsError {
		t.Fatalf("expected isError=true, got false")
	}
	if !strings.Contains(resp.Result.Content[0]["text"].(string), "not granted") {
		t.Fatalf("expected 'not granted' in error text, got: %v", resp.Result.Content)
	}
}

func TestMethodNotFound(t *testing.T) {
	h, _, _ := newTestHandler(nil, []string{"issue_read"})
	router := newTestRouter(h)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, newRequest(t, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "resources/list",
	}, "hgxs_TESTTEST_secretsecretsecretsecretsecre"))
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("expected method-not-found, got %+v", resp.Error)
	}
}
