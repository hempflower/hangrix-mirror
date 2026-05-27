package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

// stubAgentAPI panics on any method call so we can detect unexpected API
// calls from validation paths. Validation that passes calls CreateComment,
// so we override that.
type stubAgentAPI struct {
	createComment func(ctx context.Context, p *apidomain.Actor, body, filePath string, line int) (any, error)
}

func (s *stubAgentAPI) ReadIssue(ctx context.Context, p *apidomain.Actor) (any, error)             { panic("unexpected") }
func (s *stubAgentAPI) EditIssue(ctx context.Context, p *apidomain.Actor, title, body *string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) ReadIssueByNumber(ctx context.Context, p *apidomain.Actor, n int64) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) CreateIssue(ctx context.Context, p *apidomain.Actor, title, body string, parent bool) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) CreateComment(ctx context.Context, p *apidomain.Actor, body, filePath string, line int) (any, error) {
	if s.createComment != nil {
		return s.createComment(ctx, p, body, filePath, line)
	}
	panic("unexpected")
}
func (s *stubAgentAPI) GetComment(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) ListChildren(ctx context.Context, p *apidomain.Actor) (any, error)         { panic("unexpected") }
func (s *stubAgentAPI) ListChecks(ctx context.Context, p *apidomain.Actor) (any, error)           { panic("unexpected") }
func (s *stubAgentAPI) ListTodos(ctx context.Context, p *apidomain.Actor) (any, error)            { panic("unexpected") }
func (s *stubAgentAPI) CreateTodo(ctx context.Context, p *apidomain.Actor, content, status string, position int) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) UpdateTodo(ctx context.Context, p *apidomain.Actor, todoID int64, status string, content *string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) ListContributions(ctx context.Context, p *apidomain.Actor, includeClosed, includeMerged bool) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) ReadContribution(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) SetContributionMeta(ctx context.Context, p *apidomain.Actor, id int64, title, description string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) ApplyContribution(ctx context.Context, p *apidomain.Actor, id int64, message string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) CloseContribution(ctx context.Context, p *apidomain.Actor, id int64, reason string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) CreateReview(ctx context.Context, p *apidomain.Actor, contributionID int64, value, reason string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) GetMergeability(ctx context.Context, p *apidomain.Actor) (any, error)  { panic("unexpected") }
func (s *stubAgentAPI) MergeIssue(ctx context.Context, p *apidomain.Actor, message string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) CloseIssue(ctx context.Context, p *apidomain.Actor, reason string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) ListSessions(ctx context.Context, p *apidomain.Actor) (any, error)      { panic("unexpected") }
func (s *stubAgentAPI) RecoverSession(ctx context.Context, p *apidomain.Actor, sessionID int64) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) UploadAttachment(ctx context.Context, p *apidomain.Actor, data []byte, name, displayName string, inline bool, commentID int64) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) GetMe(ctx context.Context, p *apidomain.Actor) (any, error) { panic("unexpected") }
func (s *stubAgentAPI) CreateRelease(ctx context.Context, p *apidomain.Actor, tagName, title, notes string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) UpdateRelease(ctx context.Context, p *apidomain.Actor, id int64, tagName, title, notes *string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) DeleteRelease(ctx context.Context, p *apidomain.Actor, id int64) error { panic("unexpected") }
func (s *stubAgentAPI) PublishRelease(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) UploadReleaseAsset(ctx context.Context, p *apidomain.Actor, releaseID int64, name, contentB64, contentType string) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) CreateQuestionnaire(ctx context.Context, p *apidomain.Actor, input apidomain.CreateQuestionnaireInput) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) GetQuestionnaire(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) GetQuestionnaireResult(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) ListQuestionnaires(ctx context.Context, p *apidomain.Actor) (any, error) {
	panic("unexpected")
}
func (s *stubAgentAPI) CloseQuestionnaire(ctx context.Context, p *apidomain.Actor, id int64, reason string) (any, error) {
	panic("unexpected")
}

// writeAllowed is a PermissionSet that grants write.
type writeAllowed struct{}

func (writeAllowed) Can(resource, action string) bool { return true }

// actorWithWrite builds a minimal Actor with write permission.
func actorWithWrite() *apidomain.Actor {
	return &apidomain.Actor{
		SubjectKind: "agent_session",
		RoleKey:     "server",
		Permissions: writeAllowed{},
	}
}

// withActor injects a *apidomain.Actor into the request context.
func withActor(r *http.Request, a *apidomain.Actor) *http.Request {
	ctx := context.WithValue(r.Context(), ctxKeyActor, a)
	return r.WithContext(ctx)
}

// --- v1CreateComment tests ---

func TestV1CreateComment_BodyTooLong_Returns422(t *testing.T) {
	// 4001 ASCII characters → should fail before calling API.
	body := strings.Repeat("x", 4001)
	reqBody, _ := json.Marshal(map[string]any{"body": body})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/1/issues/42/comments", bytes.NewReader(reqBody))
	req = withActor(req, actorWithWrite())

	rr := httptest.NewRecorder()
	handler := v1CreateComment(&stubAgentAPI{})
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp["message"] != "comment body too long: 4001 runes (limit 4000)" {
		t.Errorf("message = %q", resp["message"])
	}

	errors, ok := resp["errors"].([]any)
	if !ok || len(errors) == 0 {
		t.Fatalf("errors missing or empty: %v", resp["errors"])
	}
	fe := errors[0].(map[string]any)
	if fe["code"] != "too_long" {
		t.Errorf("errors[0].code = %q, want too_long", fe["code"])
	}
	if fe["field"] != "body" {
		t.Errorf("errors[0].field = %q, want body", fe["field"])
	}
	if fe["resource"] != "comment" {
		t.Errorf("errors[0].resource = %q, want comment", fe["resource"])
	}
	msg, ok := fe["message"].(string)
	if !ok || !strings.Contains(msg, "Split") || !strings.Contains(msg, "4001") || !strings.Contains(msg, "4000") {
		t.Errorf("errors[0].message = %q, want Split + 4001 + 4000", msg)
	}
}

func TestV1CreateComment_Exactly4000Runes_Succeeds(t *testing.T) {
	body := strings.Repeat("x", 4000)
	reqBody, _ := json.Marshal(map[string]any{"body": body})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/1/issues/42/comments", bytes.NewReader(reqBody))
	req = withActor(req, actorWithWrite())

	rr := httptest.NewRecorder()
	called := false
	api := &stubAgentAPI{
		createComment: func(ctx context.Context, p *apidomain.Actor, b, fp string, l int) (any, error) {
			called = true
			return map[string]any{"id": 1}, nil
		},
	}
	handler := v1CreateComment(api)
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatal("CreateComment was not called for a valid body")
	}
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
}

func TestV1CreateComment_ChineseRunes_UnderLimit_Succeeds(t *testing.T) {
	// 4000 Chinese characters (12000 bytes) → should succeed.
	body := strings.Repeat("中", 4000)
	reqBody, _ := json.Marshal(map[string]any{"body": body})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/1/issues/42/comments", bytes.NewReader(reqBody))
	req = withActor(req, actorWithWrite())

	rr := httptest.NewRecorder()
	called := false
	api := &stubAgentAPI{
		createComment: func(ctx context.Context, p *apidomain.Actor, b, fp string, l int) (any, error) {
			called = true
			return map[string]any{"id": 1}, nil
		},
	}
	handler := v1CreateComment(api)
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatal("CreateComment was not called for 4000 Chinese characters")
	}
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
}

func TestV1CreateComment_EmptyBody_ReturnsMissing(t *testing.T) {
	// Empty body should still return "missing", not "too_long".
	reqBody, _ := json.Marshal(map[string]any{"body": ""})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/1/issues/42/comments", bytes.NewReader(reqBody))
	req = withActor(req, actorWithWrite())

	rr := httptest.NewRecorder()
	handler := v1CreateComment(&stubAgentAPI{})
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	errors, ok := resp["errors"].([]any)
	if !ok || len(errors) == 0 {
		t.Fatalf("errors missing or empty: %v", resp["errors"])
	}
	fe := errors[0].(map[string]any)
	if fe["code"] != "missing" {
		t.Errorf("errors[0].code = %q, want missing", fe["code"])
	}
}
