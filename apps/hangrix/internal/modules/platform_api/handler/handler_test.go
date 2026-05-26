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

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
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

type stubRegistry struct {
	tools []*apidomain.Tool
}

func (s *stubRegistry) ByName(name string) *apidomain.Tool {
	for _, t := range s.tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

func (s *stubRegistry) FilterForSession(_ *runnerdomain.AgentSession) []*apidomain.Tool {
	out := make([]*apidomain.Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t)
	}
	return out
}

func (s *stubRegistry) UploadAttachment(_ context.Context, _ *runnerdomain.AgentSession, _ []byte, _, _ string, _ bool, _ int64) (apidomain.Result, error) {
	return apidomain.Result{Text: `{"uploaded":true}`}, nil
}

func newTestRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func newTestHandler(tools []*apidomain.Tool, canList []string) (*Handler, *runnerdomain.AgentSession) {
	roleCfg, _ := json.Marshal(map[string]any{"can": canList})
	sess := &runnerdomain.AgentSession{
		ID:         42,
		RoleKey:    "backend",
		RoleConfig: roleCfg,
	}
	validator := &stubValidator{validToken: "hgxs_TESTTEST_secretsecretsecretsecretsecre", session: sess}
	reg := &stubRegistry{tools: tools}
	return NewHandlerWithRegistry(reg, validator), sess
}

func postCall(t *testing.T, router http.Handler, name string, args any, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/tools/"+name, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestCallRejectsMissingToken(t *testing.T) {
	h, _ := newTestHandler(nil, nil)
	rr := postCall(t, newTestRouter(h), "issue_read", map[string]any{}, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rr.Code)
	}
}

func TestCallRejectsInvalidToken(t *testing.T) {
	h, _ := newTestHandler(nil, nil)
	rr := postCall(t, newTestRouter(h), "issue_read", map[string]any{}, "hgxs_BADTOKEN_garbagegarbagegarbagegarbage")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403", rr.Code)
	}
}

func TestListReturnsRegistry(t *testing.T) {
	tool := &apidomain.Tool{
		Name:        "issue_read",
		Description: "read issue",
		InputSchema: map[string]any{"type": "object"},
	}
	h, _ := newTestHandler([]*apidomain.Tool{tool}, []string{"issue_read"})
	req := httptest.NewRequest(http.MethodGet, "/api/agent/tools/", nil)
	req.Header.Set("Authorization", "Bearer hgxs_TESTTEST_secretsecretsecretsecretsecre")
	rr := httptest.NewRecorder()
	newTestRouter(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tools) != 1 || resp.Tools[0]["name"] != "issue_read" {
		t.Fatalf("tools = %+v", resp.Tools)
	}
}

func TestCallDispatchesToImpl(t *testing.T) {
	called := false
	echoTool := &apidomain.Tool{
		Name:        "issue_comment",
		Description: "post",
		InputSchema: map[string]any{"type": "object"},
		Call: func(_ context.Context, _ *runnerdomain.AgentSession, args json.RawMessage) (apidomain.Result, error) {
			called = true
			return apidomain.Result{Text: `{"echoed":` + string(args) + `}`}, nil
		},
	}
	h, _ := newTestHandler([]*apidomain.Tool{echoTool}, []string{"issue_comment"})
	rr := postCall(t, newTestRouter(h), "issue_comment", map[string]any{"body": "hello"}, "hgxs_TESTTEST_secretsecretsecretsecretsecre")
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatalf("tool.Call was not invoked")
	}
	var resp callResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IsError {
		t.Fatalf("is_error unexpectedly true: %+v", resp)
	}
	if !strings.Contains(resp.Text, `"body":"hello"`) {
		t.Fatalf("echoed args missing: %v", resp.Text)
	}
}

func TestCallDeniedByRoleCanList(t *testing.T) {
	mergeTool := &apidomain.Tool{
		Name:        "issue_merge",
		Description: "merge",
		InputSchema: map[string]any{"type": "object"},
		Call: func(_ context.Context, _ *runnerdomain.AgentSession, _ json.RawMessage) (apidomain.Result, error) {
			t.Fatalf("issue_merge should not have been invoked")
			return apidomain.Result{}, nil
		},
	}
	// Role only grants issue_read — issue_merge must be rejected by the
	// per-role ACL even though it lives in the registry.
	h, _ := newTestHandler([]*apidomain.Tool{mergeTool}, []string{"issue_read"})
	rr := postCall(t, newTestRouter(h), "issue_merge", map[string]any{}, "hgxs_TESTTEST_secretsecretsecretsecretsecre")
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (soft errors come back as is_error)", rr.Code)
	}
	var resp callResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.IsError {
		t.Fatalf("expected is_error=true, got false")
	}
	if !strings.Contains(resp.Text, "not granted") {
		t.Fatalf("expected 'not granted' in text, got: %q", resp.Text)
	}
}

func TestCallUnknownTool(t *testing.T) {
	h, _ := newTestHandler(nil, []string{"issue_read"})
	rr := postCall(t, newTestRouter(h), "issue_read", map[string]any{}, "hgxs_TESTTEST_secretsecretsecretsecretsecre")
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var resp callResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.IsError || !strings.Contains(resp.Text, "unknown tool") {
		t.Fatalf("expected unknown-tool soft error, got %+v", resp)
	}
}
