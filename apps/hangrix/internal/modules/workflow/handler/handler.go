// Package handler exposes the HTTP surface for workflow management, viewing,
// dispatch, and runner callback endpoints.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// Handler implements server.RouteProvider for the workflow module.
type Handler struct {
	svc            *service.Service
	middleware     authdomain.Middleware
	agentValidator runnerdomain.AgentValidator
	repoStore      repodomain.Store
	orgResolver    orgdomain.Resolver
}

// HandlerDeps wires the handler's dependencies through ioc.
type HandlerDeps struct {
	Service        *service.Service
	Middleware     authdomain.Middleware
	AgentValidator runnerdomain.AgentValidator
	RepoStore      repodomain.Store
	OrgResolver    orgdomain.Resolver
}

// NewHandler creates a ready-to-use workflow Handler.
func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		svc:            deps.Service,
		middleware:     deps.Middleware,
		agentValidator: deps.AgentValidator,
		repoStore:      deps.RepoStore,
		orgResolver:    deps.OrgResolver,
	}
}

// RegisterRoutes implements server.RouteProvider.
func (h *Handler) RegisterRoutes(r chi.Router) {
	// User-facing API: requires auth
	r.Route("/api/repos/{owner}/{name}", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/workflow-runs", h.listRuns)
		r.Get("/workflow-runs/{runID}", h.getRun)
		r.Post("/workflow-runs", h.dispatch)
		r.Get("/workflow-runs/{runID}/jobs/{jobID}/logs", h.getLogs)
	})

	// Runner callback API: bearer hgxr_ token
	r.Route("/api/runner/workflow-jobs/{jobRunID}", func(r chi.Router) {
		r.Use(h.requireAgentToken)
		r.Post("/running", h.markJobRunning)
		r.Post("/logs", h.appendLog)
		r.Post("/terminate", h.terminateJob)
	})
}

// ---- runner auth middleware ----

type workflowCtxKey struct{ name string }

var runnerCtxKey = workflowCtxKey{"runner"}

// requireAgentToken validates the bearer hgxr_ token and injects the runner
// into the request context. It follows the same pattern as the runner
// module's AgentHandler.requireAgentToken.
func (h *Handler) requireAgentToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, err := bearerToken(r)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		runner, err := h.agentValidator.ValidateAgentToken(r.Context(), tok)
		if err != nil {
			switch {
			case errors.Is(err, runnerdomain.ErrInvalidToken):
				httpx.WriteError(w, http.StatusUnauthorized, "invalid token")
			case errors.Is(err, runnerdomain.ErrTokenInactive):
				httpx.WriteError(w, http.StatusForbidden, "token inactive")
			default:
				httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		ctx := context.WithValue(r.Context(), runnerCtxKey, runner)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// runnerFromContext extracts the authenticated runner from the request context.
func runnerFromContext(ctx context.Context) *runnerdomain.Runner {
	v, _ := ctx.Value(runnerCtxKey).(*runnerdomain.Runner)
	return v
}

// bearerToken extracts the bearer token from an Authorization header.
func bearerToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", errors.New("missing Authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", errors.New("Authorization header must use Bearer scheme")
	}
	tok := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	if tok == "" {
		return "", errors.New("bearer token is empty")
	}
	return tok, nil
}

// ---- request/response DTOs ----

type runDTO struct {
	ID           int64  `json:"id"`
	RepoID       int64  `json:"repo_id"`
	WorkflowName string `json:"workflow_name"`
	SourceFile   string `json:"source_file"`
	Status       string `json:"status"`
	EventName    string `json:"event_name"`
	CauseID      *int64 `json:"cause_id"`
	Ref          string `json:"ref"`
	CommitSHA    string `json:"commit_sha"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	CreatedAt    string `json:"created_at"`
}

type jobRunDTO struct {
	ID               int64   `json:"id"`
	WorkflowRunID    int64   `json:"workflow_run_id"`
	JobKey           string  `json:"job_key"`
	DisplayName      string  `json:"display_name"`
	Status           string  `json:"status"`
	SequenceIndex    int32   `json:"sequence_index"`
	WorkingDirectory string  `json:"working_directory"`
	TimeoutMinutes   int32   `json:"timeout_minutes"`
	RunnerID         *int64  `json:"runner_id"`
	ContainerID      *string `json:"container_id"`
	StartedAt        *string `json:"started_at"`
	FinishedAt       *string `json:"finished_at"`
	ExitCode         *int32  `json:"exit_code"`
	ErrorMessage     string  `json:"error_message"`
	CreatedAt        string  `json:"created_at"`
}

type logLineDTO struct {
	ID              int64  `json:"id"`
	WorkflowJobRunID int64 `json:"workflow_job_run_id"`
	Stream          string `json:"stream"`
	Line            string `json:"line"`
	CreatedAt       string `json:"created_at"`
}

type runListResp struct {
	Items []runDTO `json:"items"`
	Total int64    `json:"total"`
}

type runDetailResp struct {
	Run  runDTO     `json:"run"`
	Jobs []jobRunDTO `json:"jobs"`
}

type logsResp struct {
	Lines []logLineDTO `json:"lines"`
	Total int64        `json:"total"`
}

type dispatchReq struct {
	WorkflowName string            `json:"workflow_name"`
	Ref          string            `json:"ref"`
	Inputs       []dispatchInputDTO `json:"inputs"`
}

type dispatchInputDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ---- mappers ----

func toRunDTO(r *domain.WorkflowRun) runDTO {
	dto := runDTO{
		ID:           r.ID,
		RepoID:       r.RepoID,
		WorkflowName: r.WorkflowName,
		SourceFile:   r.SourceFile,
		Status:       string(r.Status),
		EventName:    string(r.EventName),
		CauseID:      r.CauseID,
		Ref:          r.Ref,
		CommitSHA:    r.CommitSHA,
		CreatedAt:    r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if r.StartedAt != nil {
		s := r.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.StartedAt = &s
	}
	if r.FinishedAt != nil {
		s := r.FinishedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.FinishedAt = &s
	}
	return dto
}

func toJobRunDTO(j *domain.WorkflowJobRun) jobRunDTO {
	dto := jobRunDTO{
		ID:               j.ID,
		WorkflowRunID:    j.WorkflowRunID,
		JobKey:           j.JobKey,
		DisplayName:      j.DisplayName,
		Status:           string(j.Status),
		SequenceIndex:    j.SequenceIndex,
		WorkingDirectory: j.WorkingDirectory,
		TimeoutMinutes:   j.TimeoutMinutes,
		RunnerID:         j.RunnerID,
		ContainerID:      j.ContainerID,
		ExitCode:         j.ExitCode,
		ErrorMessage:     j.ErrorMessage,
		CreatedAt:        j.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if j.StartedAt != nil {
		s := j.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.StartedAt = &s
	}
	if j.FinishedAt != nil {
		s := j.FinishedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.FinishedAt = &s
	}
	return dto
}

func toLogLineDTO(l *domain.WorkflowJobLogLine) logLineDTO {
	return logLineDTO{
		ID:              l.ID,
		WorkflowJobRunID: l.WorkflowJobRunID,
		Stream:          string(l.Stream),
		Line:            l.Line,
		CreatedAt:       l.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// ---- helpers ----

// resolveRepo parses owner/name from the request URL, resolves the owner
// through orgdomain.Resolver, and fetches the repo from repo.Store.
func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (*repodomain.Repo, bool) {
	ownerName := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "name")

	owner, err := h.orgResolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOwnerNotFound) || errors.Is(err, orgdomain.ErrOrgReserved) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}

	repo, err := h.repoStore.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(owner.Kind), owner.ID, repoName)
	if err != nil {
		if errors.Is(err, repodomain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}

	return repo, true
}

// ---- user-facing endpoints ----

// GET /api/repos/{owner}/{name}/workflow-runs
func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}

	workflowName := r.URL.Query().Get("workflow")

	offset := int32(0)
	limit := int32(50)
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.ParseInt(o, 10, 32); err == nil {
			offset = int32(v)
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.ParseInt(l, 10, 32); err == nil && v > 0 && v <= 200 {
			limit = int32(v)
		}
	}

	runs, total, err := h.svc.ListRunsByRepo(r.Context(), repo.ID, workflowName, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]runDTO, len(runs))
	for i, run := range runs {
		items[i] = toRunDTO(run)
	}

	httpx.WriteJSON(w, http.StatusOK, runListResp{Items: items, Total: total})
}

// GET /api/repos/{owner}/{name}/workflow-runs/{runID}
func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := httpx.ParseID(w, chi.URLParam(r, "runID"))
	if !ok {
		return
	}

	run, err := h.svc.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, domain.ErrRunNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "run not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jobs, err := h.svc.ListJobRuns(r.Context(), runID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jobDTOs := make([]jobRunDTO, len(jobs))
	for i, j := range jobs {
		jobDTOs[i] = toJobRunDTO(j)
	}

	httpx.WriteJSON(w, http.StatusOK, runDetailResp{
		Run:  toRunDTO(run),
		Jobs: jobDTOs,
	})
}

// POST /api/repos/{owner}/{name}/workflow-runs
func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}

	var req dispatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if req.WorkflowName == "" {
		httpx.WriteError(w, http.StatusBadRequest, "workflow_name is required")
		return
	}

	inputs := make([]service.DispatchInput, len(req.Inputs))
	for i, in := range req.Inputs {
		inputs[i] = service.DispatchInput{Name: in.Name, Value: in.Value}
	}

	repoRef := service.Ref{
		ID:            repo.ID,
		Name:          repo.Name,
		DefaultBranch: repo.DefaultBranch,
		OwnerName:     repo.OwnerName,
	}

	run, jobs, err := h.svc.Dispatch(r.Context(), repoRef, req.WorkflowName, inputs, req.Ref)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jobDTOs := make([]jobRunDTO, len(jobs))
	for i, j := range jobs {
		jobDTOs[i] = toJobRunDTO(j)
	}

	httpx.WriteJSON(w, http.StatusCreated, runDetailResp{
		Run:  toRunDTO(run),
		Jobs: jobDTOs,
	})
}

// GET /api/repos/{owner}/{name}/workflow-runs/{runID}/jobs/{jobID}/logs
func (h *Handler) getLogs(w http.ResponseWriter, r *http.Request) {
	jobID, ok := httpx.ParseID(w, chi.URLParam(r, "jobID"))
	if !ok {
		return
	}

	offset := int32(0)
	limit := int32(100)
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.ParseInt(o, 10, 32); err == nil {
			offset = int32(v)
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.ParseInt(l, 10, 32); err == nil && v > 0 && v <= 1000 {
			limit = int32(v)
		}
	}

	lines, total, err := h.svc.ListLogs(r.Context(), jobID, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	dto := make([]logLineDTO, len(lines))
	for i, l := range lines {
		dto[i] = toLogLineDTO(l)
	}

	httpx.WriteJSON(w, http.StatusOK, logsResp{Lines: dto, Total: total})
}

// ---- runner callback endpoints ----

// POST /api/runner/workflow-jobs/{jobRunID}/running
func (h *Handler) markJobRunning(w http.ResponseWriter, r *http.Request) {
	jobID, ok := httpx.ParseID(w, chi.URLParam(r, "jobRunID"))
	if !ok {
		return
	}

	runner := runnerFromContext(r.Context())
	if runner == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "runner not authenticated")
		return
	}

	if err := h.svc.MarkJobRunning(r.Context(), jobID, runner.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type appendLogReq struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}

// POST /api/runner/workflow-jobs/{jobRunID}/logs
func (h *Handler) appendLog(w http.ResponseWriter, r *http.Request) {
	jobID, ok := httpx.ParseID(w, chi.URLParam(r, "jobRunID"))
	if !ok {
		return
	}

	var req appendLogReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	stream := domain.LogStream(strings.TrimSpace(req.Stream))
	if stream != domain.LogStreamStdout && stream != domain.LogStreamStderr && stream != domain.LogStreamSystem {
		httpx.WriteError(w, http.StatusBadRequest, "stream must be stdout, stderr, or system")
		return
	}

	if err := h.svc.AppendLog(r.Context(), jobID, stream, req.Line); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type terminateJobReq struct {
	Status   string `json:"status"`
	ExitCode *int32 `json:"exit_code"`
	Message  string `json:"message"`
}

// POST /api/runner/workflow-jobs/{jobRunID}/terminate
func (h *Handler) terminateJob(w http.ResponseWriter, r *http.Request) {
	jobID, ok := httpx.ParseID(w, chi.URLParam(r, "jobRunID"))
	if !ok {
		return
	}

	var req terminateJobReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	status := domain.JobStatus(strings.TrimSpace(req.Status))
	switch status {
	case domain.JobStatusSuccess, domain.JobStatusFailed, domain.JobStatusCancelled:
		// valid
	default:
		httpx.WriteError(w, http.StatusBadRequest, "status must be success, failed, or cancelled")
		return
	}

	if err := h.svc.MarkJobTerminal(r.Context(), jobID, status, req.ExitCode, req.Message); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Advance the run (next job or mark terminal)
	job, err := h.svc.GetJobRun(r.Context(), jobID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.svc.AdvanceRun(r.Context(), job.WorkflowRunID); err != nil {
		// Log but don't fail — the run advancement is best-effort
		// The run will still be marked correctly on next job claim
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---- compile-time check ----
var _ server.RouteProvider = (*Handler)(nil)
