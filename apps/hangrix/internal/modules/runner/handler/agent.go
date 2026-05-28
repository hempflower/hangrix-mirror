// AgentHandler exposes the HTTP surface the standalone `hangrix-runner`
// binary speaks. Every request carries either an `hgxe_` enroll token
// (only on /enroll) or an `hgxr_` agent token (on every other route).
//
// The protocol is intentionally pull-based: the runner long-polls /tasks
// for new sessions, posts heartbeats periodically, and forwards agent
// stdout one frame at a time on /sessions/{id}/messages. Inbound stdin
// frames for the agent are claimed via /sessions/{id}/inputs. Nothing on
// this surface requires the runner to expose an inbound port.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/pkg/actor"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	repoDomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/binaries"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// pollWait caps how long /tasks and /inputs block when there is nothing
// to hand out. The runner side uses ~25s as the request timeout; pick 20s
// so the server returns 204 before that. Spec'd here (not configurable)
// because both sides need to agree.
const pollWait = 20 * time.Second
const pollTick = 500 * time.Millisecond

type AgentHandler struct {
	repo               domain.Repo
	agentValidator     domain.AgentValidator
	enrollValidator    domain.EnrollValidator
	box                *cryptobox.Box
	cfg                *config.Config
	variables          repoDomain.VariableStore
	repoStore          repoDomain.Store
	workflowDispatcher workflowdomain.Dispatcher
}

type AgentHandlerDeps struct {
	Repo               domain.Repo
	AgentValidator     domain.AgentValidator
	EnrollValidator    domain.EnrollValidator
	Config             *config.Config
	Variables          repoDomain.VariableStore
	RepoStore          repoDomain.Store
	WorkflowDispatcher workflowdomain.Dispatcher
}

func NewAgentHandler(deps *AgentHandlerDeps) *AgentHandler {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(err)
	}
	return &AgentHandler{
		repo:               deps.Repo,
		agentValidator:     deps.AgentValidator,
		enrollValidator:    deps.EnrollValidator,
		box:                box,
		cfg:                deps.Config,
		variables:          deps.Variables,
		repoStore:          deps.RepoStore,
		workflowDispatcher: deps.WorkflowDispatcher,
	}
}

func (h *AgentHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/runner", func(r chi.Router) {
		// /enroll uses the enroll-token path. It is the only route that
		// trades a token (hgxe_ in → hgxr_ out); every other route
		// requires the long-lived agent token.
		r.Post("/enroll", h.enroll)

		r.Route("/", func(r chi.Router) {
			r.Use(h.requireAgentToken)
			r.Get("/bootstrap", h.bootstrap)
			r.Get("/binaries", h.listBinaries)
			r.Get("/binaries/{name}", h.serveBinary)
			r.Post("/heartbeat", h.heartbeat)
			r.Get("/tasks", h.pollTasks)
			r.Post("/sessions/{sid}/running", h.markRunning)
			r.Post("/sessions/{sid}/messages", h.appendMessage)
			r.Get("/sessions/{sid}/inputs", h.pollInputs)
			r.Get("/sessions/{sid}/history", h.getHistory)
			r.Post("/sessions/{sid}/terminate", h.terminate)
			r.Put("/sessions/{sid}/container", h.setContainer)
			r.Post("/sessions/{sid}/ping", h.pingSession)
			r.Get("/cleanup-tasks", h.listCleanupTasks)
			r.Post("/cleanup-tasks/{sid}/done", h.markCleanupDone)
			r.Get("/stop-tasks", h.listStopTasks)
			r.Post("/stop-tasks/{sid}/done", h.markStopDone)
		})
	})

	// Public install path. Both routes are unauthenticated: the
	// install script is just a curl|sh entrypoint that does not yet
	// have an agent token, and the binary itself is a public release
	// artefact — possessing it without an enroll token still gets the
	// operator nowhere.
	r.Get("/install/runner.sh", h.serveInstallScript)
	// Anonymous binary download keyed by asset name
	// (hangrix-runner_<goos>_<goarch>). The install script picks the
	// right asset off `uname -m`.
	r.Get("/install/{asset}", h.serveInstallBinary)
}

// ---- middleware ----

type ctxKey struct{ name string }

var runnerCtxKey = ctxKey{"runner"}

func (h *AgentHandler) requireAgentToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, err := bearerToken(r)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		runner, err := h.agentValidator.ValidateAgentToken(r.Context(), tok)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrInvalidToken):
				httpx.WriteError(w, http.StatusUnauthorized, "invalid token")
			case errors.Is(err, domain.ErrTokenInactive):
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

func runnerFromContext(ctx context.Context) *domain.Runner {
	v, _ := ctx.Value(runnerCtxKey).(*domain.Runner)
	return v
}

// ---- enroll ----

type enrollReq struct {
	EnrollToken  string          `json:"enroll_token"`
	Capabilities json.RawMessage `json:"capabilities"`
}

type enrollResp struct {
	RunnerID   int64            `json:"runner_id"`
	RunnerName string           `json:"runner_name"`
	AgentToken string           `json:"agent_token"`
	Bootstrap  bootstrapPayload `json:"bootstrap"`
}

// bootstrapPayload is everything the runner needs after enrollment so it
// can persist a complete state file and start serving with no flags.
//
// The runner re-fetches this via GET /api/runner/bootstrap on every
// startup (authed with the agent token) so an operator who rotates
// endpoints / image defaults / agent binary doesn't have to touch the
// runner.
type bootstrapPayload struct {
	// Binaries is the catalogue of `hangrix-runner` artefacts embedded
	// in this server build, one per (GOOS, GOARCH). Keyed by AssetName
	// (`hangrix-runner_<goos>_<goarch>`) so the runner can pick its
	// own entry for a self-update by looking up its own GOOS/GOARCH.
	//
	// The agent binary used to ride this map; now it ships inside the
	// runner itself, so the server no longer serves it.
	Binaries map[string]binaryInfo `json:"binaries"`

	// BaseURL is the single platform base the in-container agent
	// uses to reach every backend route — LLM proxy + platform REST API.
	// The runner forwards it as HANGRIX_PLATFORM_BASE_URL; the agent
	// derives `<base>/api/llm/v1/responses` for chat completions and
	// `<base>/api/v1/...` for platform REST calls.
	BaseURL string `json:"base_url"`

	// DefaultAgentImage is what the runner falls back to when a session
	// arrives with no image override. The real session-spawn path drives
	// this per-role from the host repo's `.hangrix/agents.yml`.
	DefaultAgentImage string `json:"default_agent_image,omitempty"`

	// Cadence the runner should match. Mirrors the server-side pollWait
	// constant minus a small safety margin.
	PollWaitSec  int `json:"poll_wait_sec"`
	HeartbeatSec int `json:"heartbeat_sec"`
}

// binaryInfo is the per-platform metadata block embedded in bootstrap
// and /api/runner/binaries. URL is server-relative so the runner can
// prepend whichever base it already trusts.
type binaryInfo struct {
	URL    string `json:"url"`
	Name   string `json:"name"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func (h *AgentHandler) enroll(w http.ResponseWriter, r *http.Request) {
	var req enrollReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.EnrollToken = strings.TrimSpace(req.EnrollToken)
	if req.EnrollToken == "" {
		httpx.WriteError(w, http.StatusBadRequest, "enroll_token required")
		return
	}
	caps := []byte(req.Capabilities)
	if len(caps) == 0 {
		caps = []byte("{}")
	}
	out, err := h.enrollValidator.RedeemEnrollment(r.Context(), req.EnrollToken, caps)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidToken):
			httpx.WriteError(w, http.StatusUnauthorized, "invalid enroll token")
		case errors.Is(err, domain.ErrEnrollUsed):
			httpx.WriteError(w, http.StatusConflict, "enrollment already redeemed")
		case errors.Is(err, domain.ErrRunnerDisabled):
			httpx.WriteError(w, http.StatusForbidden, "runner disabled")
		default:
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, enrollResp{
		RunnerID:   out.Runner.ID,
		RunnerName: out.Runner.Name,
		AgentToken: out.AgentTokenPlaintext,
		Bootstrap:  h.buildBootstrap(r),
	})
}

// ---- bootstrap + agent binary ----

func (h *AgentHandler) bootstrap(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, h.buildBootstrap(r))
}

// buildBootstrap centralises the bootstrap payload assembly so /enroll
// and /bootstrap return identical shapes — the runner is allowed to
// trust either one as authoritative.
//
// BaseURL is the platform's public base; the agent (one container per
// session) appends its own paths — `/api/llm/v1/responses` for chat
// completions, `/api/v1/...` for platform REST calls. Routing both
// off a single value keeps the bootstrap shape small and the agent's
// view of "where the platform lives" minimal.
func (h *AgentHandler) buildBootstrap(r *http.Request) bootstrapPayload {
	return bootstrapPayload{
		Binaries:          h.binariesInfo(),
		BaseURL:           h.publicBase(r),
		DefaultAgentImage: h.cfg.Runner.DefaultAgentImage,
		PollWaitSec:       int(pollWait / time.Second),
		HeartbeatSec:      20,
	}
}

// publicBase decides what URL the in-container agent should talk to. In
// order of preference:
//
//  1. config.Server.URL explicitly set by the operator (production).
//  2. Reconstructed from the inbound request (devcontainer happy path).
func (h *AgentHandler) publicBase(r *http.Request) string {
	if b := strings.TrimSpace(h.cfg.Server.URL); b != "" {
		return strings.TrimRight(b, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	return scheme + "://" + host
}

// binariesInfo snapshots the embed metadata for every payload the server
// can serve. Missing payloads (operator forgot `npm run embed-binaries`)
// simply don't appear in the map — the runner's enroll path turns that
// into an actionable error.
//
// Keys are AssetNames (`hangrix-runner_linux_amd64`, …) so the runner
// can do a single `binaries[my_asset_name]` lookup keyed by its own
// runtime GOOS/GOARCH.
func (h *AgentHandler) binariesInfo() map[string]binaryInfo {
	out := map[string]binaryInfo{}
	for _, b := range binaries.All() {
		out[b.AssetName] = binaryInfo{
			URL:    "/api/runner/binaries/" + b.AssetName,
			Name:   b.Name,
			GOOS:   b.GOOS,
			GOARCH: b.GOARCH,
			SHA256: b.SHA256,
			Size:   b.Size,
		}
	}
	return out
}

func (h *AgentHandler) listBinaries(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"binaries": h.binariesInfo()})
}

// serveBinary streams an embedded runner binary. Path param `name` is
// the AssetName (`hangrix-runner_<goos>_<goarch>`); the same endpoint
// answers for every variant the build embedded.
func (h *AgentHandler) serveBinary(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	info, err := binaries.GetByAssetName(name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "binary not embedded")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Hangrix-SHA256", info.SHA256)
	http.ServeContent(w, r, info.AssetName, time.Time{}, bytes.NewReader(info.Bytes))
}

// ---- heartbeat ----

type heartbeatReq struct {
	Capabilities json.RawMessage `json:"capabilities"`
}

func (h *AgentHandler) heartbeat(w http.ResponseWriter, r *http.Request) {
	runner := runnerFromContext(r.Context())
	if runner == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "no runner in context")
		return
	}
	var req heartbeatReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	caps := []byte(req.Capabilities)
	if err := h.repo.UpdateRunnerHeartbeat(r.Context(), runner.ID, caps); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- task dispatch ----

type taskResp struct {
	// Kind discriminates the task type. "agent_session" is the default
	// (empty for backward compatibility); "workflow_job" triggers the
	// runner's WorkflowJobDriver.
	Kind string `json:"kind,omitempty"`

	// HostRepoID is the owning repo for this task. The runner uses it to
	// namespace named volumes per-repo so that two repos' "pnpm-store"
	// volumes don't collide. Always populated for both agent sessions and
	// workflow jobs.
	HostRepoID int64 `json:"host_repo_id"`

	// ---- agent_session fields ----

	SessionID       int64           `json:"session_id"`
	AgentImage      string          `json:"agent_image"`
	AgentEntrypoint []string        `json:"agent_entrypoint,omitempty"`
	AgentBuild      *agentBuildSpec `json:"agent_build,omitempty"`
	Role            string          `json:"role"`
	Model           string          `json:"model,omitempty"`
	// LLMMaxContextTokens is the resolved max_context_tokens from
	// agents.yml (role.llm.max_context_tokens or team-level fallback).
	// The runner surfaces it as HANGRIX_LLM_MAX_CONTEXT_TOKENS; the
	// agent uses 80% of it as the default compact_session threshold.
	// Zero means "not configured" — the agent falls back to 80000.
	LLMMaxContextTokens int `json:"llm_max_context_tokens,omitempty"`
	// IssueNumber is the per-repo issue this session is bound to. The runner
	// surfaces it as HANGRIX_ISSUE_NUMBER so the agent can build its
	// contribution-branch ref (issue-<N>/<role>/<slug>) — the same number
	// feeds the git ACL namespace, so the agent's branch always matches the
	// ref prefix it's allowed to push. Zero for non-issue sessions.
	IssueNumber   int32             `json:"issue_number,omitempty"`
	WorkingBranch string            `json:"working_branch"`
	BaseBranch    string            `json:"base_branch"`
	HostAddendum  string            `json:"host_addendum"`
	Env           map[string]string `json:"env"`
	// SessionToken is the plaintext hgxs_ value the runner must place in
	// HANGRIX_SESSION_TOKEN. Decrypted server-side; transmitted over the
	// runner's authenticated channel only.
	SessionToken string `json:"session_token,omitempty"`
	// ContainerID is the long-lived container the runner previously
	// created for this session (empty for a fresh session, or after the
	// 7-day idle reaper cleared it). When set, the orchestrator reuses
	// it via `docker exec`; container state survives across triggers.
	ContainerID string `json:"container_id,omitempty"`
	// RepoVariables carries the repo-level variable and secret values
	// (secrets already decrypted server-side) available for ${VAR_NAME}
	// expansion in the session's Env values. Keys are variable names;
	// values are the plaintext.
	//
	// Nil means the server has not been upgraded to support repo variable
	// expansion — the runner treats this as a backward-compat no-op and
	// leaves ${...} references unexpanded.  An empty non-nil map means the
	// server supports expansion but the repo has no variables, so any
	// ${...} reference in task.Env fails the session explicitly.
	RepoVariables map[string]string `json:"repo_variables"`
	// Volumes carries the named volume cache mounts from the host repo's
	// agents.yml container block. The runner adds each as a `-v` bind
	// mount at `docker create` time. Nil/empty means no volumes.
	Volumes []volumeDTO `json:"volumes,omitempty"`
	// McpServers is the role's MCP server whitelist extracted from the
	// frozen role_config snapshot. When non-empty, the agent only loads
	// the listed servers from .mcp.json; when nil/empty, no MCP servers
	// are loaded at all.
	McpServers []string `json:"mcp_servers,omitempty"`

	// ---- workflow_job fields (present when Kind == "workflow_job") ----

	// WorkflowJob is populated when Kind == "workflow_job".
	WorkflowJob *workflowJobDTO `json:"workflow_job,omitempty"`
}

// workflowJobDTO mirrors the runner's client.WorkflowJob wire shape.
type workflowJobDTO struct {
	JobRunID       int64                `json:"job_run_id"`
	WorkflowRunID  int64                `json:"workflow_run_id"`
	RepoID         int64                `json:"repo_id"`
	Owner          string               `json:"owner"`
	Name           string               `json:"name"`
	WorkflowName   string               `json:"workflow_name"`
	JobKey         string               `json:"job_key"`
	CheckoutRef    string               `json:"checkout_ref"`
	CommitSHA      string               `json:"commit_sha"`
	Tag            string               `json:"tag,omitempty"`
	EventName      string               `json:"event_name,omitempty"`
	EventCauseID   string               `json:"event_cause_id,omitempty"`
	Container      workflowContainerDTO `json:"container"`
	WorkingDir     string               `json:"working_directory"`
	Steps          []workflowStepDTO    `json:"steps"`
	TimeoutMinutes int                  `json:"timeout_minutes"`
	RepoVariables  map[string]string    `json:"repo_variables"`
	Inputs         map[string]string    `json:"inputs,omitempty"`
	WorkflowToken  string               `json:"workflow_token,omitempty"`
	// TriggerActorKind, TriggerActorID, TriggerActorDisplayName carry the
	// actor identity of whoever triggered this workflow run (e.g. the user
	// who pushed, or the commenter who mentioned a workflow). These are
	// surfaced to the runner as HANGRIX_TRIGGER_ACTOR_* env vars.
	TriggerActorKind        string `json:"trigger_actor_kind,omitempty"`
	TriggerActorID          string `json:"trigger_actor_id,omitempty"`
	TriggerActorDisplayName string `json:"trigger_actor_display_name,omitempty"`
}

// workflowContainerDTO mirrors the runner's client.WorkflowContainer.
type workflowContainerDTO struct {
	Image      string            `json:"image"`
	Build      *agentBuildSpec   `json:"build,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Env        map[string]string `json:"env"`
	Volumes    []volumeDTO       `json:"volumes"`
}

// workflowStepDTO mirrors the runner's client.WorkflowStep.
type workflowStepDTO struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	// Shell-step fields (type run / omitted).
	Run string            `json:"run,omitempty"`
	Env map[string]string `json:"env,omitempty"`
	Dir string            `json:"dir,omitempty"`
	// With carries built-in typed-step params (e.g. release), interpreted
	// by the runner per step type.
	With map[string]any `json:"with,omitempty"`
}

// pollTasks blocks up to pollWait waiting for a pending session pinned to
// this runner, or a pending workflow job. 204 means "no work yet"; the runner
// re-polls. 200 carries the task payload (agent session or workflow job).
func (h *AgentHandler) pollTasks(w http.ResponseWriter, r *http.Request) {
	runner := runnerFromContext(r.Context())
	if runner == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "no runner in context")
		return
	}

	deadline := time.Now().Add(pollWait)
	for {
		// 1. Try claiming an agent session first (existing path).
		sess, err := h.repo.ClaimNextSession(r.Context(), runner.ID)
		if err == nil {
			// Decrypt the session token plaintext for this dispatch.
			var plaintext string
			if sess.SessionTokenSealed != "" {
				p, derr := h.box.Decrypt(sess.SessionTokenSealed)
				if derr != nil {
					_ = h.repo.MarkSessionTerminal(r.Context(), sess.ID, domain.SessionStatusFailed, nil, "decrypt session token: "+derr.Error())
					httpx.WriteError(w, http.StatusInternalServerError, "decrypt session token")
					return
				}
				plaintext = p
			}
			env := map[string]string{}
			if len(sess.Env) > 0 {
				_ = json.Unmarshal(sess.Env, &env)
			}
			// Fetch repo-level variables/secrets for ${VAR_NAME} expansion.
			var repoVars map[string]string
			if sess.RepoID != nil {
				vars, err := h.variables.List(r.Context(), *sess.RepoID)
				if err != nil {
					httpx.WriteError(w, http.StatusInternalServerError, "list repo variables: "+err.Error())
					return
				}
				repoVars = make(map[string]string, len(vars))
				for _, v := range vars {
					// Skip entries whose ciphertext could not be decrypted.
					if v.DecryptionFailed {
						continue
					}
					repoVars[v.Name] = v.Value
				}
			}
			var hostRepoID int64
			if sess.RepoID != nil {
				hostRepoID = *sess.RepoID
			}
			var issueNumber int32
			if sess.IssueNumber != nil {
				issueNumber = *sess.IssueNumber
			}
			httpx.WriteJSON(w, http.StatusOK, taskResp{
				HostRepoID:          hostRepoID,
				SessionID:           sess.ID,
				AgentImage:          sess.AgentImage,
				AgentEntrypoint:     extractEntrypoint(sess.RoleConfig),
				AgentBuild:          extractBuild(sess.RoleConfig),
				Role:                sess.Role,
				Model:               sess.Model,
				LLMMaxContextTokens: extractLLMMaxContextTokens(sess.RoleConfig),
				IssueNumber:         issueNumber,
				WorkingBranch:       sess.WorkingBranch,
				BaseBranch:          sess.BaseBranch,
				HostAddendum:        sess.HostAddendum,
				Env:                 env,
				SessionToken:        plaintext,
				ContainerID:         sess.ContainerID,
				RepoVariables:       repoVars,
				Volumes:             extractVolumes(sess.RoleConfig),
				McpServers:          extractMcpServers(sess.RoleConfig),
			})
			return
		}
		if !errors.Is(err, domain.ErrNoPendingSession) {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// 2. No agent session; fall through to workflow job claim.
		if h.workflowDispatcher != nil {
			job, err := h.workflowDispatcher.ClaimNextJob(r.Context(), runner.ID)
			if err == nil {
				dto, err := h.buildWorkflowJobDTO(r.Context(), job)
				if err != nil {
					httpx.WriteError(w, http.StatusInternalServerError, "build workflow job: "+err.Error())
					return
				}
				httpx.WriteJSON(w, http.StatusOK, taskResp{
					Kind:        "workflow_job",
					HostRepoID:  dto.RepoID,
					WorkflowJob: dto,
				})
				return
			}
			if !errors.Is(err, workflowdomain.ErrNoPendingJob) {
				httpx.WriteError(w, http.StatusInternalServerError, "claim workflow job: "+err.Error())
				return
			}
		}

		// Nothing pending; tick until the deadline or context cancel.
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			w.WriteHeader(http.StatusNoContent)
			return
		case <-time.After(pollTick):
		}
	}
}

// buildWorkflowJobDTO assembles the workflow job dispatch payload from a
// claimed WorkflowJobRun and its parent WorkflowRun. It deserialises the
// frozen EnvJSON, StepsJSON, and ContainerSnapshotJSON, resolves the repo
// metadata, and fetches repo-level variables.
func (h *AgentHandler) buildWorkflowJobDTO(ctx context.Context, job *workflowdomain.WorkflowJobRun) (*workflowJobDTO, error) {
	// Get parent workflow run.
	run, err := h.workflowDispatcher.GetRunForJob(ctx, job.ID)
	if err != nil {
		return nil, fmt.Errorf("get run for job %d: %w", job.ID, err)
	}

	// Resolve repo metadata (owner name, repo name).
	repo, err := h.repoStore.GetByID(ctx, run.RepoID)
	if err != nil {
		return nil, fmt.Errorf("get repo %d: %w", run.RepoID, err)
	}

	// Deserialise the frozen env map (merged container ← workflow ← job).
	var env map[string]string
	if len(job.EnvJSON) > 0 {
		if err := json.Unmarshal(job.EnvJSON, &env); err != nil {
			return nil, fmt.Errorf("deserialise job env: %w", err)
		}
	}
	if env == nil {
		env = make(map[string]string)
	}

	// Deserialise the frozen steps list.
	var steps []workflowStepDTO
	if len(job.StepsJSON) > 0 {
		if err := json.Unmarshal(job.StepsJSON, &steps); err != nil {
			return nil, fmt.Errorf("deserialise job steps: %w", err)
		}
	}

	// Deserialise the container snapshot from the parent run.
	var snap workflowdomain.ContainerSnapshot
	if len(run.ContainerSnapshotJSON) > 0 {
		if err := json.Unmarshal(run.ContainerSnapshotJSON, &snap); err != nil {
			return nil, fmt.Errorf("deserialise container snapshot: %w", err)
		}
	}

	// Build container DTO.
	container := workflowContainerDTO{
		Image:      snap.Image,
		Entrypoint: snap.Entrypoint,
		Env:        env,
		Volumes:    make([]volumeDTO, 0, len(snap.Volumes)),
	}
	if snap.Build != nil {
		container.Build = &agentBuildSpec{
			Dockerfile: snap.Build.Dockerfile,
			Context:    snap.Build.Context,
			Args:       snap.Build.Args,
		}
	}
	for _, vol := range snap.Volumes {
		container.Volumes = append(container.Volumes, volumeDTO{
			Name:  vol.Name,
			Mount: vol.Mount,
		})
	}

	// Fetch repo-level variables for ${VAR_NAME} expansion.
	var repoVars map[string]string
	vars, err := h.variables.List(ctx, run.RepoID)
	if err == nil && vars != nil {
		repoVars = make(map[string]string, len(vars))
		for _, v := range vars {
			if v.DecryptionFailed {
				continue
			}
			repoVars[v.Name] = v.Value
		}
	}

	// Extract dispatch inputs from the trigger payload (only non-empty for
	// workflow.dispatch events).
	var inputs map[string]string
	if len(run.TriggerPayloadJSON) > 0 {
		var trigger struct {
			DispatchInputs map[string]string `json:"dispatch_inputs"`
		}
		if err := json.Unmarshal(run.TriggerPayloadJSON, &trigger); err == nil {
			inputs = trigger.DispatchInputs
		}
	}

	// Build cause ID string for the runner.
	var causeID string
	if run.CauseID != nil {
		causeID = strconv.FormatInt(*run.CauseID, 10)
	}

	// Extract short tag name for repo.push_tag events (run.Ref is
	// "refs/tags/<name>" for tags, "refs/heads/<name>" for branches).
	var tag string
	if run.EventName == workflowdomain.EventRepoPushTag && strings.HasPrefix(run.Ref, "refs/tags/") {
		tag = strings.TrimPrefix(run.Ref, "refs/tags/")
	}

	// Derive trigger actor from the run's event and trigger payload.
	triggerKind, triggerID, triggerDisplay := triggerActorFromRun(run)

	return &workflowJobDTO{
		JobRunID:       job.ID,
		WorkflowRunID:  run.ID,
		RepoID:         run.RepoID,
		Owner:          repo.OwnerName,
		Name:           repo.Name,
		WorkflowName:   run.WorkflowName,
		JobKey:         job.JobKey,
		CheckoutRef:    run.Ref,
		CommitSHA:      run.CommitSHA,
		Tag:            tag,
		EventName:      string(run.EventName),
		EventCauseID:   causeID,
		Container:      container,
		WorkingDir:     job.WorkingDirectory,
		Steps:          steps,
		TimeoutMinutes: int(job.TimeoutMinutes),
		RepoVariables:  repoVars,
		Inputs:         inputs,
		WorkflowToken:  run.WorkflowToken,
		TriggerActorKind:        triggerKind,
		TriggerActorID:          triggerID,
		TriggerActorDisplayName: triggerDisplay,
	}, nil
}

// triggerActorFromRun derives the trigger actor (kind, id, display_name)
// from a workflow run's event and trigger payload. For push events it reads
// pusher_user_id / pusher_agent_role from the payload; for other event types
// it falls back to system.
func triggerActorFromRun(run *workflowdomain.WorkflowRun) (kind, id, display string) {
	if len(run.TriggerPayloadJSON) == 0 {
		return string(actor.KindSystem), "system:server", "System"
	}
	var payload struct {
		PusherUserID    int64  `json:"pusher_user_id"`
		PusherAgentRole string `json:"pusher_agent_role"`
		AuthorID        int64  `json:"author_id"`
		AuthorUsername  string `json:"author_username"`
		AgentRole       string `json:"agent_role"`
	}
	if err := json.Unmarshal(run.TriggerPayloadJSON, &payload); err != nil {
		return string(actor.KindSystem), "system:server", "System"
	}
	if payload.PusherAgentRole != "" {
		ref := actor.AgentRef(payload.PusherAgentRole)
		return string(ref.Kind), ref.ID, ref.DisplayName
	}
	if payload.PusherUserID > 0 {
		ref := actor.UserRef(payload.PusherUserID, "")
		return string(ref.Kind), ref.ID, ref.DisplayName
	}
	if payload.AgentRole != "" {
		ref := actor.AgentRef(payload.AgentRole)
		return string(ref.Kind), ref.ID, ref.DisplayName
	}
	if payload.AuthorID > 0 {
		display := payload.AuthorUsername
		ref := actor.UserRef(payload.AuthorID, display)
		return string(ref.Kind), ref.ID, ref.DisplayName
	}
	return string(actor.KindSystem), "system:server", "System"
}

// ---- session lifecycle ----

func (h *AgentHandler) markRunning(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	if err := h.repo.MarkSessionRunning(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrSessionStateInvalid) {
			httpx.WriteError(w, http.StatusConflict, "session not in claimed/running state")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type terminateReq struct {
	Status      string `json:"status"`
	ExitCode    *int32 `json:"exit_code,omitempty"`
	Message     string `json:"message,omitempty"`
	RunningJobs int32  `json:"running_jobs,omitempty"`
}

func (h *AgentHandler) terminate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	var req terminateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	status := domain.SessionStatus(strings.TrimSpace(req.Status))
	// `idle` is the runner's "container exited cleanly but the session
	// stays reusable" signal — routes to MarkSessionIdle which preserves
	// the sealed token so the next trigger can rewake the row. Every
	// other accepted status is genuinely terminal.
	var err error
	switch {
	case status == domain.SessionStatusIdle:
		err = h.repo.MarkSessionIdle(r.Context(), id, req.ExitCode, req.RunningJobs)
	case status.Terminal():
		err = h.repo.MarkSessionTerminal(r.Context(), id, status, req.ExitCode, req.Message)
	default:
		httpx.WriteError(w, http.StatusBadRequest, "status must be terminal or idle")
		return
	}
	if err != nil {
		if errors.Is(err, domain.ErrSessionStateInvalid) {
			httpx.WriteError(w, http.StatusConflict, "session not in a state that accepts this transition")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- message forwarding ----

// appendMessageReq mirrors ipc.Outbound minus stream-only fields the
// runner shouldn't re-shape. The runner forwards each agent stdout frame
// as one POST; the platform persists it as the next seq in the message
// log. Per-call latency matters less than ordering, so the runner posts
// these serially in stdout-arrival order.
type appendMessageReq struct {
	Kind       string          `json:"kind"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content,omitempty"`
	Phase      string          `json:"phase,omitempty"`
	Level      string          `json:"level,omitempty"`
	Msg        string          `json:"msg,omitempty"`
	Name       string          `json:"name,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCallDTO   `json:"tool_calls,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
}

type toolCallDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (h *AgentHandler) appendMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	var req appendMessageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	kind := domain.MessageKind(strings.TrimSpace(req.Kind))
	if !kind.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid kind")
		return
	}
	m := &domain.Message{
		SessionID: id,
		Kind:      kind,
		Role:      req.Role,
		Content:   req.Content,
	}
	// Capture kind-specific fields into payload JSON so audit + replay
	// remain lossless without widening the column set.
	payload := map[string]any{}
	switch kind {
	case domain.MessageKindStatus:
		payload["phase"] = req.Phase
	case domain.MessageKindLog:
		payload["level"] = req.Level
		payload["msg"] = req.Msg
	case domain.MessageKindToolCall:
		m.ToolCallID = req.ToolCallID
		m.ToolName = req.Name
		if len(req.Args) > 0 {
			payload["args"] = rawOrNull(req.Args)
		}
		if len(req.Result) > 0 {
			payload["result"] = rawOrNull(req.Result)
		}
	case domain.MessageKindMessage:
		if len(req.ToolCalls) > 0 {
			payload["tool_calls"] = req.ToolCalls
		}
	case domain.MessageKindDone:
		payload["turn_id"] = req.TurnID
	}
	if len(payload) > 0 {
		body, _ := json.Marshal(payload)
		m.Payload = body
	}
	if _, err := h.repo.AppendMessage(r.Context(), m); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- input polling ----

type inputsResp struct {
	Frames []json.RawMessage `json:"frames"`
}

// pollInputs is the inverse of pollTasks at session granularity: long-poll
// for any new inbound IPC frames the platform has enqueued for this
// session. The runner concatenates frames as JSON-Lines onto the agent's
// stdin. Empty queues return 200 with an empty array after pollWait —
// the runner keeps the connection short rather than truly idle so it can
// notice agent exit promptly.
func (h *AgentHandler) pollInputs(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	deadline := time.Now().Add(pollWait)
	for {
		rows, err := h.repo.ClaimPendingInputs(r.Context(), id, 50)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(rows) > 0 {
			frames := make([]json.RawMessage, 0, len(rows))
			for _, in := range rows {
				frames = append(frames, rawOrNull(in.Payload))
			}
			httpx.WriteJSON(w, http.StatusOK, inputsResp{Frames: frames})
			return
		}
		if time.Now().After(deadline) {
			httpx.WriteJSON(w, http.StatusOK, inputsResp{Frames: []json.RawMessage{}})
			return
		}
		select {
		case <-r.Context().Done():
			httpx.WriteJSON(w, http.StatusOK, inputsResp{Frames: []json.RawMessage{}})
			return
		case <-time.After(pollTick):
		}
	}
}

// historyResp wraps the seed history frame the agent's loop reads as its
// mandatory first inbound. The frame is returned as raw JSON so the runner
// can write it to the agent's stdin verbatim — no re-encode round-trip.
type historyResp struct {
	Frame json.RawMessage `json:"frame"`
}

// getHistory returns the seed `kind:history` frame for a session. The
// runner calls this exactly once per agent process boot (every container
// launch and every docker-exec into a reused container), and writes the
// frame onto the agent's stdin before starting the /inputs shipper. This
// is what makes the agent's "first frame must be history" invariant hold
// across container restarts, runner restarts, and crash-mid-event paths
// that previously left the inputs queue mismatched against the agent's
// boot expectations.
//
// History today is always seeded empty; faithful turn-by-turn replay of
// the message log is M9. Moving the source here means runner code stays
// the same when replay lands — only this handler grows a message read.
func (h *AgentHandler) getHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := httpx.ParseID(w, chi.URLParam(r, "sid")); !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, historyResp{
		Frame: json.RawMessage(`{"kind":"history","messages":[]}`),
	})
}

// ---- container lifecycle ----

type setContainerReq struct {
	ContainerID string `json:"container_id"`
}

// setContainer records the long-lived container id the runner created (or
// reattached to) for this session. The runner posts this once per agent
// run, right after orchestrator.Start. We also bump container_last_used_at
// in the same UPDATE — that timestamp feeds the 7-day idle reaper. The
// caller's runner_id is not validated against sess.RunnerID here because
// ClaimNextSession already pinned the session to this runner; a misrouted
// PUT could only land on a session the runner doesn't own if its agent
// token leaked, in which case the cleanup_pending flag (cleared by a
// separate, runner-scoped query) is the safety net.
func (h *AgentHandler) setContainer(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	var req setContainerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	cid := strings.TrimSpace(req.ContainerID)
	if cid == "" {
		httpx.WriteError(w, http.StatusBadRequest, "container_id required")
		return
	}
	if err := h.repo.SetSessionContainer(r.Context(), id, cid); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "session not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pingSession bumps container_last_used_at so roster_list last_activity_at
// reflects real-time agent liveness. The runner calls this on every agent
// interaction (tool call, thinking, output).
func (h *AgentHandler) pingSession(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	if err := h.repo.PingSession(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "session not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type cleanupTaskDTO struct {
	SessionID   int64  `json:"session_id"`
	ContainerID string `json:"container_id"`
}

type cleanupTasksResp struct {
	Tasks []cleanupTaskDTO `json:"tasks"`
}

// listCleanupTasks returns up to 50 (session, container) pairs the
// platform has flagged for this runner to `docker rm`. The partial
// index keeps it O(flagged rows owned by this runner) so the runner can
// poll cheaply on a short interval. Empty list returns 200 with an empty
// array (not 204) so a polling client can treat it as a successful poll.
func (h *AgentHandler) listCleanupTasks(w http.ResponseWriter, r *http.Request) {
	runner := runnerFromContext(r.Context())
	if runner == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "no runner in context")
		return
	}
	tasks, err := h.repo.ListPendingContainerCleanups(r.Context(), runner.ID, 50)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := cleanupTasksResp{Tasks: make([]cleanupTaskDTO, 0, len(tasks))}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, cleanupTaskDTO{SessionID: t.SessionID, ContainerID: t.ContainerID})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// markCleanupDone is the runner's ACK that `docker rm` of the session's
// container succeeded (or that the container was already gone — see
// DockerOrchestrator.RemoveContainer's idempotent path). Scoped by
// runner_id at the SQL layer so a runner that doesn't own the session
// can't clear another runner's column even with a leaked agent token.
func (h *AgentHandler) markCleanupDone(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	runner := runnerFromContext(r.Context())
	if runner == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "no runner in context")
		return
	}
	if err := h.repo.ClearSessionContainer(r.Context(), id, runner.ID); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			// The session may have been deleted between the runner's
			// listCleanupTasks and this ACK. Treat as success — the
			// container is gone either way and the flag is moot.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- stop tasks (migration 00005) ----

type stopTaskDTO struct {
	SessionID   int64  `json:"session_id"`
	ContainerID string `json:"container_id"`
}

type stopTasksResp struct {
	Tasks []stopTaskDTO `json:"tasks"`
}

// listStopTasks returns up to 50 (session, container) pairs the
// platform has flagged for this runner to `docker stop`. Mirrors the
// existing cleanup-tasks endpoint shape.
func (h *AgentHandler) listStopTasks(w http.ResponseWriter, r *http.Request) {
	runner := runnerFromContext(r.Context())
	if runner == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "no runner in context")
		return
	}
	tasks, err := h.repo.ListPendingContainerStops(r.Context(), runner.ID, 50)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := stopTasksResp{Tasks: make([]stopTaskDTO, 0, len(tasks))}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, stopTaskDTO{SessionID: t.SessionID, ContainerID: t.ContainerID})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// markStopDone is the runner's ACK that `docker stop` of the session's
// container succeeded. Scoped by runner_id at the SQL layer so a runner
// that doesn't own the session can't clear another runner's column.
func (h *AgentHandler) markStopDone(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	runner := runnerFromContext(r.Context())
	if runner == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "no runner in context")
		return
	}
	if err := h.repo.MarkSessionContainerStopped(r.Context(), id, runner.ID); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- token + ctx helpers ----

// agentBuildSpec mirrors agentsconfig.Build on the wire. When set on
// the dispatch response, the runner runs `docker build -f <Dockerfile>
// -t <agent_image> [--build-arg K=V ...] <context>` before
// `docker create`, materialising the image on demand. Empty / absent
// means the runner pulls / uses `agent_image` as-is.
type agentBuildSpec struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// volumeDTO mirrors agentsconfig.Volume on the wire.
type volumeDTO struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
}

// extractEntrypoint reads container.entrypoint out of the frozen
// role_config snapshot. Returns nil on any decode error or missing
// field — the runner falls back to its built-in `sleep infinity`
// default in that case. The snapshot is written by
// agent_session/service.buildRoleSnapshot.
func extractEntrypoint(roleConfig []byte) []string {
	if len(roleConfig) == 0 {
		return nil
	}
	var snap struct {
		Container struct {
			Entrypoint []string `json:"entrypoint"`
		} `json:"container"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return nil
	}
	if len(snap.Container.Entrypoint) == 0 {
		return nil
	}
	return snap.Container.Entrypoint
}

// extractVolumes reads container.volumes out of the frozen role_config
// snapshot. Returns nil when the host yaml has no volumes defined.
func extractVolumes(roleConfig []byte) []volumeDTO {
	if len(roleConfig) == 0 {
		return nil
	}
	var snap struct {
		Container struct {
			Volumes []volumeDTO `json:"volumes"`
		} `json:"container"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return nil
	}
	return snap.Container.Volumes
}

// extractMcpServers reads mcp_servers out of the frozen role_config
// snapshot. Returns nil when the role has no MCP whitelist (the agent
// defaults to loading zero MCP servers).
func extractMcpServers(roleConfig []byte) []string {
	if len(roleConfig) == 0 {
		return nil
	}
	var snap struct {
		McpServers []string `json:"mcp_servers"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return nil
	}
	return snap.McpServers

}

// extractLLMMaxContextTokens reads llm_max_context_tokens out of the frozen
// role_config snapshot. Returns 0 when the field is absent or the snapshot
// is empty — the agent will fall back to the default compact threshold.
func extractLLMMaxContextTokens(roleConfig []byte) int {
	if len(roleConfig) == 0 {
		return 0
	}
	var snap struct {
		LLMMaxContextTokens int `json:"llm_max_context_tokens"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return 0
	}
	return snap.LLMMaxContextTokens
}

// extractBuild reads container.build out of the frozen role_config
// snapshot. Returns nil when the host yaml used container.image
// (pull-only) rather than container.build (build-on-demand).
func extractBuild(roleConfig []byte) *agentBuildSpec {
	if len(roleConfig) == 0 {
		return nil
	}
	var snap struct {
		Container struct {
			Build *agentBuildSpec `json:"build"`
		} `json:"container"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return nil
	}
	if snap.Container.Build == nil || snap.Container.Build.Dockerfile == "" {
		return nil
	}
	return snap.Container.Build
}

func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("missing authorization header")
	}
	const pfx = "Bearer "
	if !strings.HasPrefix(h, pfx) {
		return "", errors.New("authorization must be Bearer")
	}
	tok := strings.TrimSpace(h[len(pfx):])
	if tok == "" {
		return "", errors.New("empty bearer token")
	}
	return tok, nil
}
