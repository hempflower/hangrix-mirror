// Package handler exposes two HTTP surfaces for the runner module:
//
//   - AdminHandler is mounted at /api/admin/runners. Cookie + RequireAdmin
//     gated. Used for runner enrollment lifecycle and admin-triggered test
//     sessions.
//
//   - AgentHandler is mounted at /api/runner. Bearer hgxe_/hgxr_ gated. The
//     `hangrix-runner` binary speaks here over plain HTTP — outbound-only,
//     no inbound port on the runner side.
//
// The admin surface returns plaintext enrollment tokens exactly once, on
// the POST response. The admin surface never returns the agent token (the
// runner receives that on /api/runner/enroll) and never returns the
// session token plaintext (the runner receives that on /api/runner/tasks
// after claiming the session). Both bearer artefacts live ONLY on the
// runner machine's disk after issuance.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	actordomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/service"
	"github.com/hangrix/hangrix/pkg/actor"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// runnerNameRe constrains the user-visible runner name to a slug. The name
// is for display only; identity lives in (id, token), so this is purely a
// "humans don't accidentally pick weird characters" guard.
var runnerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9 _.-]{0,63}$`)

type AdminHandler struct {
	repo          domain.Repo
	repos         repodomain.Store
	middleware    authdomain.Middleware
	box           *cryptobox.Box
	actorResolver actordomain.Resolver
}

type AdminHandlerDeps struct {
	Repo          domain.Repo
	Repos         repodomain.Store
	Middleware    authdomain.Middleware
	Config        *config.Config
	ActorResolver actordomain.Resolver
}

func NewAdminHandler(deps *AdminHandlerDeps) *AdminHandler {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(err)
	}
	return &AdminHandler{
		repo:          deps.Repo,
		repos:         deps.Repos,
		middleware:    deps.Middleware,
		box:           box,
		actorResolver: deps.ActorResolver,
	}
}

func (h *AdminHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/admin/runners", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)

		r.Post("/", h.createRunner)
		r.Get("/", h.listRunners)
		r.Get("/{id}", h.getRunner)
		r.Delete("/{id}", h.disableRunner)

		// Hard-delete a runner row. Use POST /disable for the soft
		// variant; this one removes the row from the listing entirely
		// (agent_sessions.runner_id is ON DELETE SET NULL so audit
		// history survives).
		r.Delete("/{id}/permanent", h.removeRunner)

		// Admin-triggered test session for smoke / debug.
		r.Post("/{id}/sessions", h.createSession)
		r.Get("/sessions/{sid}", h.getSession)
		r.Get("/sessions/{sid}/messages", h.listMessages)
	})
}

// ---- runner DTOs ----

type publicRunner struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	OwnerUserID *int64 `json:"owner_user_id,omitempty"`
	Visibility  string `json:"visibility"`
	Status      string `json:"status"`
	// Online is a derived liveness flag: true when Status=='active' AND
	// the last heartbeat is within domain.HeartbeatStaleThreshold of the
	// server clock at response time. The UI uses this to surface "offline"
	// for an admin-active runner that has stopped beating, since Status
	// itself only changes on explicit admin action.
	Online            bool        `json:"online"`
	Capabilities      interface{} `json:"capabilities"`
	LastHeartbeatAt   *time.Time  `json:"last_heartbeat_at,omitempty"`
	EnrollTokenPrefix string      `json:"enroll_token_prefix"`
	EnrollTokenUsed   bool        `json:"enroll_token_used"`
	AgentTokenPrefix  string      `json:"agent_token_prefix,omitempty"`
	AgentTokenRevoked bool        `json:"agent_token_revoked"`
	CreatedBy         int64       `json:"created_by,omitempty"`
	ActorID           int64       `json:"actor_id,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

func (h *AdminHandler) toPublicRunner(r *domain.Runner) publicRunner {
	var caps any = map[string]any{}
	if len(r.Capabilities) > 0 {
		_ = json.Unmarshal(r.Capabilities, &caps)
	}
	// Resolve actor_id → user_id for the legacy created_by JSON key.
	createdBy := int64(0)
	if h.actorResolver != nil {
		uid, ok := h.actorResolver.UserID(context.Background(), r.ActorID)
		if ok {
			createdBy = uid
		}
	}
	return publicRunner{
		ID:                r.ID,
		Name:              r.Name,
		OwnerUserID:       r.OwnerUserID,
		Visibility:        string(r.Visibility),
		Status:            string(r.Status),
		Online:            r.Online(time.Now()),
		Capabilities:      caps,
		LastHeartbeatAt:   r.LastHeartbeatAt,
		EnrollTokenPrefix: r.EnrollTokenPrefix,
		EnrollTokenUsed:   r.EnrollTokenUsedAt != nil,
		AgentTokenPrefix:  r.AgentTokenPrefix,
		AgentTokenRevoked: r.AgentTokenRevokedAt != nil,
		CreatedBy:         createdBy,
		ActorID:           r.ActorID,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
	}
}

type createRunnerReq struct {
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
}

type createRunnerResp struct {
	Runner               publicRunner `json:"runner"`
	EnrollTokenPlaintext string       `json:"enroll_token"`
}

// createRunner is admin-only. Forces visibility=platform on this path —
// user-level runner registration is a separate non-admin surface.
func (h *AdminHandler) createRunner(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createRunnerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Visibility = strings.TrimSpace(req.Visibility)
	if req.Visibility == "" {
		req.Visibility = string(domain.VisibilityPlatform)
	}
	if !runnerNameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	v := domain.Visibility(req.Visibility)
	if v == domain.VisibilityUser {
		// Admin creating a user runner on behalf of someone else is out
		// of scope here — the user-self-service surface owns that.
		httpx.WriteError(w, http.StatusBadRequest, "admin path only supports platform runners")
		return
	}
	var actorID int64
	if caller != nil && h.actorResolver != nil {
		resolved, err := h.actorResolver.From(r.Context(), actor.UserRef(caller.ID, ""))
		if err == nil {
			actorID = resolved.ActorID
		}
	}
	in := domain.CreateRunnerInput{Name: req.Name, Visibility: v, ActorID: actorID}
	if err := in.Validate(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Mint the enrollment token in service (regex+secret+bcrypt) and
	// hand only the (prefix, hash) pair to Repo. The plaintext is
	// shown once in the response and never again recoverable.
	plaintext, prefix, hashed, err := service.MintEnrollToken()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runner, err := h.repo.CreateRunner(r.Context(), in, domain.NewEnrollToken{
		Prefix: prefix,
		Hash:   string(hashed),
	})
	if err != nil {
		if errors.Is(err, domain.ErrRunnerConflict) {
			httpx.WriteError(w, http.StatusConflict, "name already taken")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, createRunnerResp{
		Runner:               h.toPublicRunner(runner),
		EnrollTokenPlaintext: plaintext,
	})
}

func (h *AdminHandler) listRunners(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListRunners(r.Context(), nil, nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicRunner, 0, len(rows))
	for _, p := range rows {
		items = append(items, h.toPublicRunner(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *AdminHandler) getRunner(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	out, err := h.repo.GetRunnerByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrRunnerNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "runner not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.toPublicRunner(out))
}

func (h *AdminHandler) disableRunner(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := h.repo.DisableRunner(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrRunnerNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "runner not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeRunner hard-deletes a runner row. agent_sessions.runner_id has
// ON DELETE SET NULL so any historical session this runner served keeps
// its row intact (the runner pointer just goes blank).
func (h *AdminHandler) removeRunner(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := h.repo.DeleteRunner(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrRunnerNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "runner not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- session DTOs ----

type createSessionReq struct {
	// Model is what the agent will pass on its LLM proxy calls. The proxy
	// resolves model → provider at request time; the admin path doesn't
	// pick a provider directly.
	Model string `json:"model"`

	// Image is the container image the runner pulls. The real session
	// path drives this from the host repo's `.hangrix/agents.yml`; this
	// admin path lets the operator override it directly for smoke tests.
	AgentImage string `json:"agent_image"`

	// Optional contextual fields surfaced into the agent's env / prompt.
	Role          string `json:"role,omitempty"`
	HostRepo      string `json:"host_repo,omitempty"`
	IssueNumber   *int32 `json:"issue_number,omitempty"`
	WorkingBranch string `json:"working_branch,omitempty"`
	BaseBranch    string `json:"base_branch,omitempty"`
	HostAddendum  string `json:"host_addendum,omitempty"`

	// MockEvent is the first inbound event the runner pushes into the
	// agent's stdin queue right after the seed history frame. The
	// platform persists it as a kind='event' message so audit shows the
	// trigger.
	MockEvent struct {
		Name    string          `json:"name"`
		Payload json.RawMessage `json:"payload"`
	} `json:"mock_event,omitempty"`

	// Optional extra env overrides — merged into the runner-built env
	// after the canonical HANGRIX_* values are set.
	ExtraEnv map[string]string `json:"extra_env,omitempty"`
}

type publicSession struct {
	ID            int64  `json:"id"`
	RunnerID      *int64 `json:"runner_id,omitempty"`
	RepoID        *int64 `json:"repo_id,omitempty"`
	IssueNumber   *int32 `json:"issue_number,omitempty"`
	Status        string `json:"status"`
	Role          string `json:"role"`
	Model         string `json:"model,omitempty"`
	AgentImage    string `json:"agent_image"`
	WorkingBranch string `json:"working_branch"`
	BaseBranch    string `json:"base_branch"`
	HostAddendum  string `json:"host_addendum"`
	// Snapshot. Populated at session-spawn; surfaced on the admin view
	// so audit consumers can verify the pin from outside the runner
	// module.
	RepoSHA      string            `json:"repo_sha,omitempty"`
	RoleKey      string            `json:"role_key,omitempty"`
	CauseKind    string            `json:"cause_kind,omitempty"`
	CauseID      string            `json:"cause_id,omitempty"`
	Env          map[string]string `json:"env"`
	ExitCode     *int32            `json:"exit_code,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	ClaimedAt    *time.Time        `json:"claimed_at,omitempty"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	EndedAt      *time.Time        `json:"ended_at,omitempty"`
}

func toPublicSession(s *domain.AgentSession) publicSession {
	env := map[string]string{}
	if len(s.Env) > 0 {
		_ = json.Unmarshal(s.Env, &env)
	}
	return publicSession{
		ID:            s.ID,
		RunnerID:      s.RunnerID,
		RepoID:        s.RepoID,
		IssueNumber:   s.IssueNumber,
		Status:        string(s.Status),
		Role:          s.Role,
		Model:         s.Model,
		AgentImage:    s.AgentImage,
		WorkingBranch: s.WorkingBranch,
		BaseBranch:    s.BaseBranch,
		HostAddendum:  s.HostAddendum,
		Env:           env,
		ExitCode:      s.ExitCode,
		ErrorMessage:  s.ErrorMessage,
		CreatedAt:     s.CreatedAt,
		ClaimedAt:     s.ClaimedAt,
		StartedAt:     s.StartedAt,
		EndedAt:       s.EndedAt,
		RepoSHA:       s.RepoSHA,
		RoleKey:       s.RoleKey,
		CauseKind:     s.CauseKind,
		CauseID:       s.CauseID,
	}
}

// createSession is the admin-triggered smoke path: mint a session token
// (agent identity, NOT an LLM-provider binding), build env, persist a
// pending session pinned to the chosen runner, and seed the inputs queue
// with (a) a history=[] frame and (b) the mock event.
//
// The minted session token plaintext is sealed onto the row so the runner
// can fetch it at claim time. We deliberately do NOT echo the plaintext
// back on the admin response: the admin user doesn't need it; the runner
// will receive it over its own authenticated channel.
func (h *AdminHandler) createSession(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	var createdByActorID int64
	if caller != nil && h.actorResolver != nil {
		resolved, err := h.actorResolver.From(r.Context(), actor.UserRef(caller.ID, ""))
		if err == nil {
			createdByActorID = resolved.ActorID
		}
	}

	runnerID, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	runnerRow, err := h.repo.GetRunnerByID(r.Context(), runnerID)
	if err != nil {
		if errors.Is(err, domain.ErrRunnerNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "runner not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runnerRow.Status == domain.StatusDisabled {
		httpx.WriteError(w, http.StatusBadRequest, "runner disabled")
		return
	}

	var req createSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	req.AgentImage = strings.TrimSpace(req.AgentImage)
	if req.Model == "" {
		httpx.WriteError(w, http.StatusBadRequest, "model is required")
		return
	}
	if req.AgentImage == "" {
		httpx.WriteError(w, http.StatusBadRequest, "agent_image is required")
		return
	}

	// Mint the session identity token. The plaintext is sealed onto the
	// row; only the runner ever sees it (when it claims this task) and
	// only the in-container agent uses it (Bearer on every platform call).
	plaintext, prefix, hashed, err := service.MintSessionToken()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sealed, err := h.box.Encrypt(plaintext)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// host_repo is "owner/name" — currently informational only on the
	// admin path; the real session-spawn orchestrator resolves it for
	// production triggers. Missing repo leaves repo_id NULL — the smoke
	// path doesn't push.
	var repoID *int64
	if req.HostRepo != "" {
		_ = repoID
	}

	in := domain.CreateSessionInput{
		RunnerID:           &runnerRow.ID,
		RepoID:             repoID,
		IssueNumber:        req.IssueNumber,
		Role:               req.Role,
		Model:              req.Model,
		AgentImage:         req.AgentImage,
		WorkingBranch:      req.WorkingBranch,
		BaseBranch:         req.BaseBranch,
		HostAddendum:       req.HostAddendum,
		Env:                req.ExtraEnv,
		SessionTokenPrefix: prefix,
		SessionTokenHash:   string(hashed),
		SessionTokenSealed: sealed,
		CreatedByActorID:   createdByActorID,
	}
	sess, err := h.repo.CreateSession(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Seed the inputs queue with the mock event if the admin supplied
	// one. The seed history frame is fetched by the runner from
	// GET /sessions/{sid}/history at agent boot — not queued here.
	if req.MockEvent.Name != "" {
		// Persist the event as a kind=event message too, so the audit
		// log shows the trigger.
		msgPayload := map[string]any{
			"event":   req.MockEvent.Name,
			"payload": rawOrNull(req.MockEvent.Payload),
		}
		body, _ := json.Marshal(msgPayload)
		if _, err := h.repo.AppendMessage(r.Context(), &domain.Message{
			SessionID: sess.ID,
			Kind:      domain.MessageKindEvent,
			EventName: req.MockEvent.Name,
			Payload:   body,
		}); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		frame := map[string]any{
			"kind":    "event",
			"event":   req.MockEvent.Name,
			"payload": rawOrNull(req.MockEvent.Payload),
		}
		frameJSON, _ := json.Marshal(frame)
		if _, err := h.repo.EnqueueInput(r.Context(), sess.ID, frameJSON); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	httpx.WriteJSON(w, http.StatusCreated, toPublicSession(sess))
}

func (h *AdminHandler) getSession(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	s, err := h.repo.GetSessionByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "session not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicSession(s))
}

type publicMessage struct {
	ID         int64           `json:"id"`
	Seq        int32           `json:"seq"`
	Kind       string          `json:"kind"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content,omitempty"`
	EventName  string          `json:"event,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (h *AdminHandler) listMessages(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "sid"))
	if !ok {
		return
	}
	rows, err := h.repo.ListMessages(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicMessage, 0, len(rows))
	for _, m := range rows {
		items = append(items, publicMessage{
			ID:         m.ID,
			Seq:        m.Seq,
			Kind:       string(m.Kind),
			Role:       m.Role,
			Content:    m.Content,
			EventName:  m.EventName,
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
			Payload:    rawOrNull(m.Payload),
			CreatedAt:  m.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ---- shared helpers ----

func rawOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}
