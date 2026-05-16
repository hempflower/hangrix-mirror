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
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/binaries"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// pollWait caps how long /tasks and /inputs block when there is nothing
// to hand out. The runner side uses ~25s as the request timeout; pick 20s
// so the server returns 204 before that. Spec'd here (not configurable)
// because both sides need to agree.
const pollWait = 20 * time.Second
const pollTick = 500 * time.Millisecond

type AgentHandler struct {
	repo            domain.Repo
	agentValidator  domain.AgentValidator
	enrollValidator domain.EnrollValidator
	box             *cryptobox.Box
	cfg             *config.Config
	// Cross-module deps used only by the M7a agent-bundle endpoint.
	// orgResolver  username/orgname → Owner{Kind,ID,Name}
	// repos        Metadata + kind validation
	// paths        bare-repo fsPath for git archive
	orgResolver orgdomain.Resolver
	repos       repodomain.Store
	paths       repodomain.PathResolver
}

type AgentHandlerDeps struct {
	Repo            domain.Repo
	AgentValidator  domain.AgentValidator
	EnrollValidator domain.EnrollValidator
	Config          *config.Config
	OrgResolver     orgdomain.Resolver
	Repos           repodomain.Store
	Paths           repodomain.PathResolver
}

func NewAgentHandler(deps *AgentHandlerDeps) *AgentHandler {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(err)
	}
	return &AgentHandler{
		repo:            deps.Repo,
		agentValidator:  deps.AgentValidator,
		enrollValidator: deps.EnrollValidator,
		box:             box,
		cfg:             deps.Config,
		orgResolver:     deps.OrgResolver,
		repos:           deps.Repos,
		paths:           deps.Paths,
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
			r.Post("/sessions/{sid}/terminate", h.terminate)
			// M7a agent bundle distribution. Trailing wildcard so
			// chi keeps the .tar.gz suffix in {*} for parseBundleRef
			// to validate (other formats are explicitly unsupported).
			r.Get("/agent-bundles/{owner}/{name}/*", h.getAgentBundle)
		})
	})
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
	// Binaries maps logical name → metadata for every artefact embedded
	// in the server build. Today: "hangrix-agent" + "hangrix-runner".
	// The runner reads "hangrix-agent" out and downloads it; "hangrix-
	// runner" is there so the runner can self-update later.
	Binaries map[string]binaryInfo `json:"binaries"`

	// In-container endpoints. The runner injects these into HANGRIX_*
	// env vars before launching the agent.
	LLMEndpoint string `json:"llm_endpoint"`
	MCPEndpoint string `json:"mcp_endpoint"`

	// DefaultAgentImage is what the runner falls back to when a session
	// arrives with no image override. M7a starts driving this per-role
	// from host repo .hangrix/agents.yml.
	DefaultAgentImage string `json:"default_agent_image,omitempty"`

	// Cadence the runner should match. Mirrors the server-side pollWait
	// constant minus a small safety margin.
	PollWaitSec  int `json:"poll_wait_sec"`
	HeartbeatSec int `json:"heartbeat_sec"`
}

// binaryInfo is the per-binary metadata block embedded in bootstrap and
// /api/runner/binaries. URL is server-relative so the runner can prepend
// whichever base it already trusts.
type binaryInfo struct {
	URL    string `json:"url"`
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
// LLMEndpoint / MCPEndpoint are fully-qualified including the API base
// path so the in-container agent can append "/responses" (LLM) or its
// MCP method directly. Centralising the path here keeps the agent
// portable: change the proxy mount point and the runner picks it up on
// the next bootstrap without an agent re-deploy.
func (h *AgentHandler) buildBootstrap(r *http.Request) bootstrapPayload {
	base := h.publicBase(r)
	return bootstrapPayload{
		Binaries:          h.binariesInfo(),
		LLMEndpoint:       base + "/api/llm/v1",
		MCPEndpoint:       base + "/api/mcp/v1",
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
// can serve. Missing payloads (operator forgot `make embed-binaries`)
// simply don't appear in the map — the runner's enroll path turns that
// into an actionable error.
func (h *AgentHandler) binariesInfo() map[string]binaryInfo {
	out := map[string]binaryInfo{}
	for _, b := range binaries.All() {
		out[b.Name] = binaryInfo{
			URL:    "/api/runner/binaries/" + b.Name,
			SHA256: b.SHA256,
			Size:   b.Size,
		}
	}
	return out
}

func (h *AgentHandler) listBinaries(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"binaries": h.binariesInfo()})
}

// serveBinary streams the embedded named binary. /api/runner/binaries/
// {hangrix-agent|hangrix-runner} — the same endpoint serves whichever
// build artefact the runner asks for, both gated by the same Bearer
// agent token.
func (h *AgentHandler) serveBinary(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	info, err := binaries.Get(name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "binary not embedded")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Hangrix-SHA256", info.SHA256)
	http.ServeContent(w, r, info.Name, time.Time{}, bytes.NewReader(info.Bytes))
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
	SessionID  int64  `json:"session_id"`
	AgentImage string `json:"agent_image"`
	Role       string `json:"role"`
	Model      string `json:"model,omitempty"`
	// AgentRepo is "<owner>/<name>@<sha>". The runner resolves the
	// `<sha>` against its content-addressed cache; on miss it pulls
	// the corresponding tarball from
	// /api/runner/agent-bundles/{owner}/{name}/{sha}.tar.gz and
	// mounts it read-only at /opt/hangrix/bundle. Empty on M6c-era
	// admin smoke sessions that don't need a bundle.
	AgentRepo     string            `json:"agent_repo"`
	WorkingBranch string            `json:"working_branch"`
	BaseBranch    string            `json:"base_branch"`
	HostAddendum  string            `json:"host_addendum"`
	Env           map[string]string `json:"env"`
	// SessionToken is the plaintext hgxs_ value the runner must place in
	// HANGRIX_SESSION_TOKEN. Decrypted server-side; transmitted over the
	// runner's authenticated channel only.
	SessionToken string `json:"session_token,omitempty"`
}

// pollTasks blocks up to pollWait waiting for a pending session pinned to
// this runner. 204 means "no work yet"; the runner re-polls. 200 carries
// the task payload including the decrypted session token plaintext.
func (h *AgentHandler) pollTasks(w http.ResponseWriter, r *http.Request) {
	runner := runnerFromContext(r.Context())
	if runner == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "no runner in context")
		return
	}

	deadline := time.Now().Add(pollWait)
	for {
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
			httpx.WriteJSON(w, http.StatusOK, taskResp{
				SessionID:     sess.ID,
				AgentImage:    sess.AgentImage,
				Role:          sess.Role,
				Model:         sess.Model,
				AgentRepo:     sess.AgentRepo,
				WorkingBranch: sess.WorkingBranch,
				BaseBranch:    sess.BaseBranch,
				HostAddendum:  sess.HostAddendum,
				Env:           env,
				SessionToken:  plaintext,
			})
			return
		}
		if !errors.Is(err, domain.ErrNoPendingSession) {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// nothing pending; tick until the deadline or context cancel.
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
	Status   string `json:"status"`
	ExitCode *int32 `json:"exit_code,omitempty"`
	Message  string `json:"message,omitempty"`
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
	if !status.Terminal() {
		httpx.WriteError(w, http.StatusBadRequest, "status must be terminal")
		return
	}
	if err := h.repo.MarkSessionTerminal(r.Context(), id, status, req.ExitCode, req.Message); err != nil {
		if errors.Is(err, domain.ErrSessionStateInvalid) {
			httpx.WriteError(w, http.StatusConflict, "session already terminal")
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

// ---- token + ctx helpers ----

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
