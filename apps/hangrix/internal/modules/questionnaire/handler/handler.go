// Package handler exposes the questionnaire module's user-facing HTTP surface
// under /api/repos/{owner}/{name}/issues/{number}/questionnaires/...
// It mirrors the issue handler pattern: cookie-auth gate, repo resolution,
// issue lookup, then delegation to the questionnaire service.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/pkg/actor"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	actordomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	questionnairedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/domain"
	questionnaireservice "github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/service"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	repoinfra "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// Handler is the HTTP handler for the user-facing questionnaire API.
type Handler struct {
	svc           questionnairedomain.Service
	issues        issuedomain.Store
	repos         repodomain.Store
	storage       *repoinfra.Storage
	resolver      orgdomain.Resolver
	middleware    authdomain.Middleware
	users         userdomain.Repo
	spawner       agentsessiondomain.Spawner    // optional — nil-safe (e.g. tests)
	actorResolver actordomain.Resolver
}

type HandlerDeps struct {
	Service       questionnairedomain.Service
	Issues        issuedomain.Store
	Repos         repodomain.Store
	Storage       *repoinfra.Storage
	Resolver      orgdomain.Resolver
	Middleware    authdomain.Middleware
	Users         userdomain.Repo
	Spawner       agentsessiondomain.Spawner    // optional — nil-safe
	ActorResolver actordomain.Resolver
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		svc:           deps.Service,
		issues:        deps.Issues,
		repos:         deps.Repos,
		storage:       deps.Storage,
		resolver:      deps.Resolver,
		middleware:    deps.Middleware,
		users:         deps.Users,
		spawner:       deps.Spawner,
		actorResolver: deps.ActorResolver,
	}
}

// RegisterRoutes mounts the questionnaire endpoints under the issue namespace.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/repos/{owner}/{name}/issues/{number}/questionnaires", func(r chi.Router) {
		r.Use(h.authGate)
		r.Get("/", h.list)
		r.Get("/{qid}", h.get)
		r.Post("/{qid}/answers", h.submit)
		r.Get("/{qid}/results", h.results)
	})
}

// authGate mirrors the issue handler's authGate: tries workflow token, falls back to cookie.
func (h *Handler) authGate(next http.Handler) http.Handler {
	// For simplicity, use the same cookie-auth approach.
	// Workflow token auth is not needed for questionnaires (only user-facing).
	return h.middleware.RequireAuth(next)
}

// ---- DTOs ---- //

type publicQuestionnaire struct {
	ID             int64                     `json:"id"`
	IssueID        int64                     `json:"issue_id"`
	Title          string                    `json:"title"`
	Description    string                    `json:"description"`
	Status         string                    `json:"status"`
	CreatedByAgent string                    `json:"created_by_agent"`
	CreatedAt      string                    `json:"created_at"`
	ClosedAt       *string                   `json:"closed_at,omitempty"`
	Questions      []publicQuestion          `json:"questions"`
	MySubmission   *publicMySubmission       `json:"my_submission,omitempty"`
}

type publicQuestion struct {
	ID       int64           `json:"id"`
	Position int             `json:"position"`
	Text     string          `json:"text"`
	Type     string          `json:"type"`
	Options  []publicOption  `json:"options,omitempty"`
	Required bool            `json:"required"`
}

type publicOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type publicMySubmission struct {
	SubmittedAt string              `json:"submitted_at"`
	Answers     []publicAnswerEntry `json:"answers"`
}

type publicAnswerEntry struct {
	QuestionID int64    `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	Text       string   `json:"text,omitempty"`
}

type publicResult struct {
	Questionnaire *publicQuestionnaire           `json:"questionnaire"`
	Submissions   int                            `json:"submissions"`
	ByQuestion    map[string]publicQuestionResult `json:"by_question"`
	Submitters    []publicSubmitter              `json:"submitters,omitempty"`
}

type publicQuestionResult struct {
	Type      string               `json:"type"`
	Tallies   []publicChoiceTally  `json:"tallies,omitempty"`
	Responses []publicTextResponse `json:"responses,omitempty"`
}

type publicChoiceTally struct {
	OptionID string  `json:"option_id"`
	Label    string  `json:"label"`
	Count    int     `json:"count"`
	Percent  float64 `json:"percent"`
}

type publicTextResponse struct {
	UserID      int64  `json:"user_id"`
	DisplayName string `json:"user_display"`
	Text        string `json:"text"`
	SubmittedAt string `json:"submitted_at"`
}

type publicSubmitter struct {
	UserID      int64               `json:"user_id"`
	DisplayName string              `json:"user_display"`
	SubmittedAt string              `json:"submitted_at"`
	Answers     []publicAnswerEntry `json:"answers"`
}

// ---- helpers ---- //

type repoCtx struct {
	repo   *repodomain.Repo
	fsPath string
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,38}$`)
var repoNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (*repoCtx, bool) {
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "name")
	if !usernameRe.MatchString(owner) || !repoNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid repo")
		return nil, false
	}
	resolved, err := h.resolver.ResolveOwner(r.Context(), owner)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "repo not found")
		return nil, false
	}
	repo, err := h.repos.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(resolved.Kind), resolved.ID, name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "repo not found")
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if repo.Visibility == repodomain.VisibilityPrivate {
		ok, err := h.canRead(r.Context(), caller, repo)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !ok {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
	}
	fsPath, err := h.storage.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
		return nil, false
	}
	return &repoCtx{repo: repo, fsPath: fsPath}, true
}

func (h *Handler) canRead(ctx context.Context, caller *userdomain.User, repo *repodomain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		return caller.ID == repo.OwnerID, nil
	case repodomain.OwnerKindOrg:
		_, ok, err := h.resolver.Membership(ctx, repo.OwnerID, caller.ID)
		return ok, err
	}
	return false, nil
}

func (h *Handler) loadIssue(w http.ResponseWriter, r *http.Request, repoID int64) (*issuedomain.Issue, bool) {
	raw := chi.URLParam(r, "number")
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid issue number")
		return nil, false
	}
	iss, err := h.issues.GetByNumber(r.Context(), repoID, n)
	if err != nil {
		if errors.Is(err, issuedomain.ErrIssueNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "issue not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return iss, true
}

// currentActorID resolves the authenticated user's user_id → actor_id via the
// actor.Resolver. Returns (0, false) for unauthenticated or unresolved callers.
func (h *Handler) currentActorID(r *http.Request) (int64, bool) {
	caller, ok := authdomain.UserFromRequest(r)
	if !ok || caller == nil {
		return 0, false
	}
	if h.actorResolver != nil {
		resolved, err := h.actorResolver.From(r.Context(), actor.UserRef(caller.ID, ""))
		if err == nil {
			return resolved.ActorID, true
		}
	}
	return 0, false
}

// ---- handlers ---- //

// GET /api/repos/{owner}/{name}/issues/{number}/questionnaires
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}

	qns, err := h.svc.GetByIssue(r.Context(), iss.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var result []publicQuestionnaire
	for _, qn := range qns {
		pq := toPublicQuestionnaire(qn)
		// Optionally include my_submission for authenticated users.
		if aid, ok := h.currentActorID(r); ok {
			if ans, err := h.svc.GetUserAnswer(r.Context(), qn.ID, aid); err == nil && ans != nil {
				pq.MySubmission = toPublicMySubmission(ans)
			}
		}
		result = append(result, pq)
	}

	if result == nil {
		result = []publicQuestionnaire{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

// GET /api/repos/{owner}/{name}/issues/{number}/questionnaires/{qid}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	_, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	// issue resolution not strictly needed but we still verify repo access above.

	qid, ok := parseQID(w, r)
	if !ok {
		return
	}

	qn, err := h.svc.Get(r.Context(), qid)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "questionnaire not found")
		return
	}

	pq := toPublicQuestionnaire(qn)
	if aid, ok := h.currentActorID(r); ok {
		if ans, err := h.svc.GetUserAnswer(r.Context(), qn.ID, aid); err == nil && ans != nil {
			pq.MySubmission = toPublicMySubmission(ans)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": pq})
}

// POST /api/repos/{owner}/{name}/issues/{number}/questionnaires/{qid}/answers
func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}

	qid, ok := parseQID(w, r)
	if !ok {
		return
	}

	aid, ok := h.currentActorID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "login required")
		return
	}

	var req struct {
		Answers []struct {
			QuestionID int64    `json:"question_id"`
			OptionIDs  []string `json:"option_ids,omitempty"`
			Text       string   `json:"text,omitempty"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Convert to per-question map.
	perQ := make(map[int64]questionnairedomain.AnswerValue, len(req.Answers))
	for _, a := range req.Answers {
		perQ[a.QuestionID] = questionnairedomain.AnswerValue{
			OptionIDs: a.OptionIDs,
			Text:      a.Text,
		}
	}

	answer, qn, err := h.svc.UpsertAnswer(r.Context(), qid, aid, perQ)
	if err != nil {
		if errors.Is(err, questionnairedomain.ErrQuestionnaireLocked) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]any{
					"code":    "questionnaire_locked",
					"message": "This questionnaire has already been answered and is no longer accepting responses.",
				},
			})
			return
		}
		var ve *questionnaireservice.ValidationError
		if errors.As(err, &ve) {
			writeValidationError(w, ve)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Wake the agent that issued this questionnaire. The spawner's
	// direct-invoke path targets exactly that role and bypasses
	// per-role trigger config — the agent doesn't need an explicit
	// questionnaire.answered entry in agents.yml.
	if h.spawner != nil && qn.CreatedByAgent != "" {
		payload, _ := json.Marshal(map[string]any{
			"questionnaire_id": qid,
			"answer_id":        answer.ID,
			"respondent_id":    aid,
		})
		issueNum, _ := strconv.ParseInt(chi.URLParam(r, "number"), 10, 32)
		_, _ = h.spawner.OnTrigger(r.Context(), agentsessiondomain.TriggerInput{
			Trigger:     agentsconfig.TriggerIssueComment,
			CauseKind:   agentsessiondomain.CauseKindQuestionnaireAnswered,
			CauseID:     strconv.FormatInt(answer.ID, 10),
			RepoID:      rc.repo.ID,
			IssueNumber: int32(issueNum),
			ActorID:     aid,
			RoleKey:     qn.CreatedByAgent,
			Payload:     payload,
		})
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"answer_id":           answer.ID,
			"submitted_at":        answer.SubmittedAt.Format("2006-01-02T15:04:05Z"),
			"questionnaire_status": string(qn.Status),
		},
	})
}

// GET /api/repos/{owner}/{name}/issues/{number}/questionnaires/{qid}/results
func (h *Handler) results(w http.ResponseWriter, r *http.Request) {
	_, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}

	qid, ok := parseQID(w, r)
	if !ok {
		return
	}

	result, err := h.svc.BuildResult(r.Context(), qid)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "questionnaire not found")
		return
	}

	pr := toPublicResult(result)
	writeJSON(w, http.StatusOK, map[string]any{"data": pr})
}

// ---- conversion helpers ---- //

func toPublicQuestionnaire(qn *questionnairedomain.Questionnaire) publicQuestionnaire {
	pq := publicQuestionnaire{
		ID:             qn.ID,
		IssueID:        qn.IssueID,
		Title:          qn.Title,
		Description:    qn.Description,
		Status:         string(qn.Status),
		CreatedByAgent: qn.CreatedByAgent,
		CreatedAt:      qn.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if qn.ClosedAt != nil {
		s := qn.ClosedAt.Format("2006-01-02T15:04:05Z")
		pq.ClosedAt = &s
	}
	for _, q := range qn.Questions {
		pq.Questions = append(pq.Questions, toPublicQuestion(q))
	}
	return pq
}

func toPublicQuestion(q questionnairedomain.Question) publicQuestion {
	pq := publicQuestion{
		ID:       q.ID,
		Position: q.Position,
		Text:     q.Text,
		Type:     string(q.Type),
		Required: q.Required,
	}
	for _, o := range q.Options {
		pq.Options = append(pq.Options, publicOption{ID: o.ID, Label: o.Label})
	}
	return pq
}

func toPublicMySubmission(a *questionnairedomain.Answer) *publicMySubmission {
	ms := &publicMySubmission{
		SubmittedAt: a.SubmittedAt.Format("2006-01-02T15:04:05Z"),
	}
	for qid, av := range a.PerQuestion {
		ms.Answers = append(ms.Answers, publicAnswerEntry{
			QuestionID: qid,
			OptionIDs:  av.OptionIDs,
			Text:       av.Text,
		})
	}
	return ms
}

func toPublicResult(r *questionnairedomain.Result) publicResult {
	pr := publicResult{
		Questionnaire: nil,
		Submissions:   r.Submissions,
		ByQuestion:    make(map[string]publicQuestionResult),
	}
	if r.Questionnaire != nil {
		pq := toPublicQuestionnaire(r.Questionnaire)
		pr.Questionnaire = &pq
	}
	for qid, qr := range r.ByQuestion {
		key := strconv.FormatInt(qid, 10)
		pqr := publicQuestionResult{Type: string(qr.Type)}
		for _, t := range qr.Tallies {
			pqr.Tallies = append(pqr.Tallies, publicChoiceTally{
				OptionID: t.OptionID, Label: t.Label,
				Count: t.Count, Percent: t.Percent,
			})
		}
		for _, tr := range qr.Responses {
			pqr.Responses = append(pqr.Responses, publicTextResponse{
				UserID: tr.UserID, DisplayName: tr.DisplayName,
				Text: tr.Text, SubmittedAt: tr.SubmittedAt.Format("2006-01-02T15:04:05Z"),
			})
		}
		pr.ByQuestion[key] = pqr
	}
	for _, sd := range r.Submitters {
		ps := publicSubmitter{
			UserID:      sd.UserID,
			DisplayName: sd.DisplayName,
			SubmittedAt: sd.SubmittedAt.Format("2006-01-02T15:04:05Z"),
		}
		for _, a := range sd.Answers {
			ps.Answers = append(ps.Answers, publicAnswerEntry{
				QuestionID: a.QuestionID,
				OptionIDs:  a.OptionIDs,
				Text:       a.Text,
			})
		}
		pr.Submitters = append(pr.Submitters, ps)
	}
	return pr
}

func writeValidationError(w http.ResponseWriter, ve *questionnaireservice.ValidationError) {
	var fieldErrors []map[string]string
	for _, e := range ve.Errors {
		fieldErrors = append(fieldErrors, map[string]string{
			"field":   e.Field,
			"code":    e.Code,
			"message": e.Message,
		})
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"message": "validation failed",
		"errors":  fieldErrors,
	})
}

// ---- helpers ---- //

func parseQID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "qid")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid questionnaire id")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
