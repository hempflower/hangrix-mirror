package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	issuegatedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue_gate/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// stubGate implements issuegatedomain.IssueActivityGate for middleware tests.
type stubGate struct {
	err error
}

func (g *stubGate) CheckIssue(_ context.Context, _ int64, _ int32) error {
	return g.err
}

func TestIssueGateMiddleware_PassThrough_NoSession(t *testing.T) {
	// No session in context → middleware should pass through.
	g := &stubGate{err: &issuegatedomain.ErrIssueTerminal{IssueNumber: 1, State: issuegatedomain.ReasonClosed}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := IssueGateMiddleware(g)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when no session, got %d", rec.Code)
	}
}

func TestIssueGateMiddleware_PassThrough_NilIssueNumber(t *testing.T) {
	// Session without IssueNumber → pass through (unbound session).
	g := &stubGate{err: &issuegatedomain.ErrIssueTerminal{IssueNumber: 1, State: issuegatedomain.ReasonClosed}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := IssueGateMiddleware(g)

	repoID := int64(1)
	sess := &runnerdomain.AgentSession{RepoID: &repoID, IssueNumber: nil} // no issue
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	ctx := context.WithValue(req.Context(), ctxKeySession, sess)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when session has no issue, got %d", rec.Code)
	}
}

func TestIssueGateMiddleware_BlocksTerminalIssue(t *testing.T) {
	g := &stubGate{err: &issuegatedomain.ErrIssueTerminal{
		RepoID:      1,
		IssueNumber: 42,
		State:       issuegatedomain.ReasonClosed,
	}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called for terminal issue")
	})
	mw := IssueGateMiddleware(g)

	repoID := int64(1)
	issueNum := int32(42)
	sess := &runnerdomain.AgentSession{RepoID: &repoID, IssueNumber: &issueNum}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	ctx := context.WithValue(req.Context(), ctxKeySession, sess)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for terminal issue, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["code"] != "issue_terminal" {
		t.Fatalf("expected code issue_terminal, got: %v", body["code"])
	}
	if body["issue_number"] != float64(42) { // JSON numbers → float64
		t.Fatalf("expected issue_number 42, got: %v", body["issue_number"])
	}
	if body["issue_state"] != "closed" {
		t.Fatalf("expected issue_state closed, got: %v", body["issue_state"])
	}
}

func TestIssueGateMiddleware_AllowsOpenIssue(t *testing.T) {
	g := &stubGate{err: nil} // open issue → no error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := IssueGateMiddleware(g)

	repoID := int64(1)
	issueNum := int32(42)
	sess := &runnerdomain.AgentSession{RepoID: &repoID, IssueNumber: &issueNum}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	ctx := context.WithValue(req.Context(), ctxKeySession, sess)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for open issue, got %d", rec.Code)
	}
}
