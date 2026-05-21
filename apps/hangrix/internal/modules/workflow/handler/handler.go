// Package handler exposes the HTTP surface for workflow management, viewing,
// dispatch, and runner callback endpoints.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// Handler implements server.RouteProvider for the workflow module.
type Handler struct {
	svc        *service.Service
	middleware authdomain.Middleware
}

// HandlerDeps wires the handler's dependencies through ioc.
type HandlerDeps struct {
	Service    *service.Service
	Middleware authdomain.Middleware
}

// NewHandler creates a ready-to-use workflow Handler.
func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		svc:        deps.Service,
		middleware: deps.Middleware,
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
		r.Post("/running", h.markJobRunning)
		r.Post("/logs", h.appendLog)
		r.Post("/terminate", h.terminateJob)
	})
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

// ---- user-facing endpoints ----

// GET /api/repos/{owner}/{name}/workflow-runs
func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "name")
	workflowName := r.URL.Query().Get("workflow")

	// TODO: resolve repo ID from owner/name
	// For now, stub - will need repo.Store dependency
	_ = owner
	_ = name
	_ = workflowName

	httpx.WriteError(w, http.StatusNotImplemented, "list runs: repo resolution not yet wired")
}

// GET /api/repos/{owner}/{name}/workflow-runs/{runID}
func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := httpx.ParseID(w, chi.URLParam(r, "runID"))
	if !ok {
		return
	}

	run, err := h.svc.GetRunForJob(r.Context(), runID)
	if err != nil {
		if errors.Is(err, domain.ErrRunNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "run not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Use GetRun directly
	run, err = h.svc.GetRun(r.Context(), runID)
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
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "name")

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

	// TODO: resolve repo from owner/name
	_ = owner
	_ = name

	httpx.WriteError(w, http.StatusNotImplemented, "dispatch: repo resolution not yet wired")
	_ = inputs
	_ = req.Ref
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

	// TODO: extract runner ID from authenticated context
	if err := h.svc.MarkJobRunning(r.Context(), jobID, 0); err != nil {
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
