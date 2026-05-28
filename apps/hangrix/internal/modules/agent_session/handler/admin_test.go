package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
)

// stubAuditor returns a pre-canned slice. The handler is a thin
// JSON-shaper around it so this is all the surface we need to fake.
type stubAuditor struct {
	rows []domain.AuditSession
	err  error
	last struct {
		repoID int64
		issue  int32
	}
}

func (s *stubAuditor) ListByIssue(_ context.Context, repoID int64, issueNumber int32) ([]domain.AuditSession, error) {
	s.last.repoID = repoID
	s.last.issue = issueNumber
	return s.rows, s.err
}

func (s *stubAuditor) GetSession(_ context.Context, _ int64) (*domain.AuditSession, error) {
	return nil, domain.ErrSessionNotFound
}

func (s *stubAuditor) ListMessages(_ context.Context, _ int64) ([]domain.SessionMessage, error) {
	return nil, nil
}

func (s *stubAuditor) ListRecent(_ context.Context, _ domain.RecentFilter) ([]domain.AuditSession, int64, error) {
	return s.rows, int64(len(s.rows)), s.err
}

// noopMiddleware satisfies authdomain.Middleware without any actual
// auth — the unit test focuses on the handler's JSON shape, not the
// auth chain. RequireAuth / RequireAdmin both just pass through.
type noopMiddleware struct{}

func (noopMiddleware) RequireAuth(next http.Handler) http.Handler  { return next }
func (noopMiddleware) RequireAdmin(next http.Handler) http.Handler { return next }

// stubController satisfies domain.Controller for tests.
type stubController struct{}

func (stubController) Stop(ctx context.Context, sessionID int64, reason string) error               { return nil }
func (stubController) Resume(ctx context.Context, sessionID int64) error                            { return nil }
func (stubController) Recover(ctx context.Context, sessionID int64, recoveredBy string) error       { return nil }
func (stubController) Delete(ctx context.Context, sessionID int64) error                            { return nil }
func (stubController) StopContainerNow(ctx context.Context, sessionID int64) error                  { return nil }
func (stubController) RemoveContainerNow(ctx context.Context, sessionID int64) error                { return nil }

func newRouter(h *AdminHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

// TestAdminListByIssueRoundTrip drives the full request → JSON →
// AuditSession round-trip. We pre-seed the stub with a representative
// row (snapshot pins + cause + role config) and assert the JSON
// matches.
func TestAdminListByIssueRoundTrip(t *testing.T) {
	row := domain.AuditSession{
		SessionID:  42,
		RepoID:     1,
		Issue:      7,
		RoleKey:    "backend",
		Status:     "pending",
		RepoSHA:    "def",
		CauseKind:  "issue_opened",
		CauseID:    "comment-1",
		RoleConfig: json.RawMessage(`{"permission":"read","model":"x"}`),
		CreatedAt:  time.Now(),
	}
	aud := &stubAuditor{rows: []domain.AuditSession{row}}

	h := NewAdminHandler(&AdminHandlerDeps{
		Auditor:    aud,
		Controller: stubController{},
		Middleware: noopMiddleware{},
	})

	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/agent-sessions/by-issue/1/7")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Items []publicAuditSession `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(body.Items))
	}
	got := body.Items[0]
	if got.SessionID != 42 || got.RoleKey != "backend" {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.RepoSHA != "def" {
		t.Fatalf("snapshot pin lost: %+v", got)
	}
	if got.CauseKind != "issue_opened" || got.CauseID != "comment-1" {
		t.Fatalf("cause fields lost: %+v", got)
	}
	// role_config flows through as a raw JSON object.
	if string(got.RoleConfig) != `{"permission":"read","model":"x"}` {
		t.Fatalf("role_config = %s", string(got.RoleConfig))
	}
	// Auditor was called with the right scoping.
	if aud.last.repoID != 1 || aud.last.issue != 7 {
		t.Fatalf("auditor called with (%d,%d), want (1,7)", aud.last.repoID, aud.last.issue)
	}
}

// TestAdminListByIssueInvalidParams: non-numeric / zero / negative path
// segments yield 400 (caller error) rather than 500. Mirrors the M6c
// admin routes' error stance.
func TestAdminListByIssueInvalidParams(t *testing.T) {
	h := NewAdminHandler(&AdminHandlerDeps{
		Auditor:    &stubAuditor{},
		Controller: stubController{},
		Middleware: noopMiddleware{},
	})
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	cases := []string{
		"/api/admin/agent-sessions/by-issue/abc/1",
		"/api/admin/agent-sessions/by-issue/1/abc",
		"/api/admin/agent-sessions/by-issue/0/1",
		"/api/admin/agent-sessions/by-issue/1/0",
		"/api/admin/agent-sessions/by-issue/-1/1",
	}
	for _, p := range cases {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400", p, resp.StatusCode)
		}
	}
}

// TestAdminListByIssueEmpty asserts the empty case returns an empty
// array (not null). Frontends iterate `items[]` directly and choke on
// a JSON null.
func TestAdminListByIssueEmpty(t *testing.T) {
	h := NewAdminHandler(&AdminHandlerDeps{
		Auditor:    &stubAuditor{rows: nil},
		Controller: stubController{},
		Middleware: noopMiddleware{},
	})
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/agent-sessions/by-issue/1/1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Items []publicAuditSession `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Items == nil {
		t.Fatalf("items is nil, want empty slice")
	}
	if len(body.Items) != 0 {
		t.Fatalf("items len = %d, want 0", len(body.Items))
	}
}

// silence unused warnings
var _ = authdomain.Middleware(noopMiddleware{})
