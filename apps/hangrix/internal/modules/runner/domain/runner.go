// Package domain declares the runner / agent-session model and the
// cross-module interfaces other packages consume. The Postgres impl in
// the sibling infra/ package satisfies Repo + EnrollValidator +
// AgentValidator + SessionTokenValidator on one type, mirroring the
// llm_provider pattern.
//
// Wire formats:
//
//	hgxe_<8>_<32>  enrollment token   (one-shot, redeemed for an agent token)
//	hgxr_<8>_<32>  runner agent token (long-lived bearer on every poll)
//	hgxs_<8>_<32>  session token      (one per agent_sessions row; represents
//	                                   agent identity — used by the in-container
//	                                   agent to call the platform LLM proxy, the
//	                                   agent HTTP API, etc. NOT coupled to any
//	                                   LLM provider)
//
// The three prefixes are distinct so a single auth router can dispatch a
// raw Authorization header to the right validator without trying every
// store.
package domain

import (
	"context"
	"errors"
	"time"
)

// Visibility splits runners into two dispatch pools. A platform runner is
// owned by no specific user (owner_user_id IS NULL) and can be scheduled
// by any session. A user runner is scheduled only for the owner's own
// sessions. Enforcement of "this caller may schedule on this runner"
// lives in the handler that mints sessions; the DB only stores the tag.
type Visibility string

const (
	VisibilityPlatform Visibility = "platform"
	VisibilityUser     Visibility = "user"
)

func (v Visibility) Valid() bool {
	return v == VisibilityPlatform || v == VisibilityUser
}

// Status is the lifecycle state of a runner row. A freshly created runner
// is "pending" until it redeems its enrollment token; from that point it
// transitions to "active" and stays there until an admin disables it.
type Status string

const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

func (s Status) Valid() bool {
	return s == StatusPending || s == StatusActive || s == StatusDisabled
}

// Runner is the platform-side view of one runner machine.
//
// Token fields hold persisted state only:
//   - EnrollTokenPrefix / EnrollTokenHash: present from creation until the
//     enrollment is redeemed; EnrollTokenUsedAt then marks the redemption.
//   - AgentTokenPrefix / AgentTokenHash: populated at redemption time and
//     long-lived. Revoking the runner sets AgentTokenRevokedAt.
//
// Capabilities is opaque JSON the runner self-reports on heartbeat — image
// pull cache state, cgroup version, cpu/mem ceilings, etc. The platform
// stores it as-is for diagnostics and does not parse it.
type Runner struct {
	ID                  int64
	Name                string
	OwnerUserID         *int64
	Visibility          Visibility
	Status              Status
	Capabilities        []byte // raw JSON; empty == "{}"
	LastHeartbeatAt     *time.Time
	EnrollTokenPrefix   string
	EnrollTokenHash     string
	EnrollTokenUsedAt   *time.Time
	AgentTokenPrefix    string
	AgentTokenHash      string
	AgentTokenRevokedAt *time.Time
	ActorID             int64   // FK to actors(id); replaces created_by
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// HeartbeatStaleThreshold is how long the platform waits after a
// runner's last_heartbeat_at before declaring it offline for liveness
// purposes. The runner heartbeats every 20s (loop.HeartbeatEvery); 60s
// covers ~3 missed ticks plus network jitter without flapping on a
// single missed beat. It does NOT affect Status — that stays admin-
// driven (active/disabled/pending). Used only to derive the runtime
// liveness flag surfaced to operators.
const HeartbeatStaleThreshold = 60 * time.Second

// Online reports whether the runner has heartbeated recently enough to
// be considered live. A disabled or never-enrolled runner is never
// online regardless of past beats; an active runner is online iff its
// last beat is within HeartbeatStaleThreshold of `now`.
func (r *Runner) Online(now time.Time) bool {
	if r.Status != StatusActive {
		return false
	}
	if r.LastHeartbeatAt == nil {
		return false
	}
	return now.Sub(*r.LastHeartbeatAt) <= HeartbeatStaleThreshold
}

// AgentTokenActive reports whether the runner currently holds a usable
// long-term token. False means: never redeemed, or revoked, or runner
// disabled. The runner-facing HTTP layer rejects any non-active runner
// with 403 regardless of whether the token bcrypt-compares.
func (r *Runner) AgentTokenActive() bool {
	if r.Status != StatusActive {
		return false
	}
	if r.AgentTokenPrefix == "" || r.AgentTokenHash == "" {
		return false
	}
	if r.AgentTokenRevokedAt != nil {
		return false
	}
	return true
}

// EnrollTokenActive reports whether the enrollment token can still be
// redeemed. Single-use semantics: once used or revoked (status=disabled)
// the answer is no.
func (r *Runner) EnrollTokenActive() bool {
	if r.Status == StatusDisabled {
		return false
	}
	if r.EnrollTokenUsedAt != nil {
		return false
	}
	return true
}

// SessionStatus is the lifecycle of one agent_sessions row.
//
// Two layers of state coexist on the same column:
//
//	pending → claimed → running                       — one container life.
//	running → succeeded | failed | cancelled          — that container ended.
//	running → idle                                    — the container finished
//	          one turn but the parent issue is still open. The row stays put
//	          waiting for the next trigger, which will recycle it back to
//	          pending (and from there through claimed → running again).
//	* → archived                                      — the parent issue
//	          closed / merged. The row is dead for good; restart means a new
//	          session on a new issue.
//
// Spec: docs/agent-config.md §"Session 模型".
type SessionStatus string

const (
	SessionStatusPending   SessionStatus = "pending"
	SessionStatusClaimed   SessionStatus = "claimed"
	SessionStatusRunning   SessionStatus = "running"
	SessionStatusSucceeded SessionStatus = "succeeded"
	SessionStatusFailed    SessionStatus = "failed"
	SessionStatusCancelled SessionStatus = "cancelled"
	SessionStatusIdle      SessionStatus = "idle"
	SessionStatusArchived  SessionStatus = "archived"
)

// Terminal reports whether the row will never run again. Archived is
// terminal (issue is gone); idle is NOT terminal (the row waits for the
// next trigger and goes back to pending). Succeeded / failed / cancelled
// describe the most recent container and are also terminal — the
// per-role session module signals "still active" with idle and
// "permanently done" with archived.
func (s SessionStatus) Terminal() bool {
	switch s {
	case SessionStatusSucceeded, SessionStatusFailed, SessionStatusCancelled, SessionStatusArchived:
		return true
	}
	return false
}

// AgentSession is one scheduled run.
//
// Every session owns exactly one session token (`hgxs_<prefix>_<secret>`).
// The token is the in-container agent's identity for every platform call
// it makes — LLM proxy, MCP server, and any future agent-facing surface.
// Storage shape mirrors PATs / runner tokens:
//
//   - SessionTokenPrefix / SessionTokenHash: bcrypt-hashed secret, used by
//     the validator to authenticate inbound requests.
//   - SessionTokenSealed: the cryptobox-sealed plaintext, decryptable only
//     by the platform. The runner fetches it at task-claim time so it can
//     inject HANGRIX_SESSION_TOKEN into the agent's env, which the inline
//     credential.helper baked into the cloned .git/config reads at request
//     time. Preserved across idle and terminal (failed / succeeded /
//     cancelled) so a rewake re-exports the same identity — cheaper than
//     minting and zero DB churn, even though the helper would in principle
//     happily pick up a freshly minted value. NULL'd only when the row is
//     archived — issue closed or user-deleted — i.e. genuinely never
//     coming back.
//   - SessionTokenRevokedAt: set when the session terminates so a leaked
//     token from a dead session can't be replayed.
type AgentSession struct {
	ID                    int64
	RunnerID              *int64
	RepoID                *int64
	IssueNumber           *int32
	Status                SessionStatus
	Role                  string
	Model                 string
	AgentImage            string
	WorkingBranch         string
	BaseBranch            string
	HostAddendum          string
	Env                   []byte // JSON map[string]string, empty == "{}"
	SessionTokenPrefix    string
	SessionTokenHash      string
	SessionTokenSealed    string
	SessionTokenRevokedAt *time.Time
	ExitCode              *int32
	ErrorMessage          string
	CreatedBy             int64
	CreatedByActorID      int64  // FK to actors(id); resolved by spawner
	CreatedAt             time.Time
	ClaimedAt             *time.Time
	StartedAt             *time.Time
	EndedAt               *time.Time

	// Snapshot fields frozen at session-spawn so a later audit can
	// reconstruct exactly which host configuration + code state produced
	// any commit made by this session. See docs/agent-config.md
	// §"Session 模型".
	RepoSHA    string // host repo base-branch sha at spawn time
	RoleKey    string // role identifier from host yaml; commit author for pushes
	CauseKind  string // trigger family (e.g. "issue_opened", "comment_mentioned")
	CauseID    string // upstream artefact id (opaque to runner)
	RoleConfig []byte // resolved role config snapshot (JSON), empty == "{}"

	// Container lifecycle (decoupled from agent-process lifecycle since
	// migration 00004). Live containers are owned by the runner_id row;
	// the platform tracks the id, last-touched timestamp, and a pending-
	// cleanup flag the runner polls for.
	//
	//   ContainerID              "" when no live container (fresh session
	//                            or container already reaped).
	//   ContainerLastUsedAt      bumped on every exec; drives the 7-day
	//                            idle reaper.
	//   ContainerCleanupPending  TRUE when the platform wants the runner
	//                            to `docker rm` this container — set on
	//                            archive, user-delete, or idle sweep.
	ContainerID             string
	ContainerLastUsedAt     *time.Time
	ContainerCleanupPending bool

	// Container stop lifecycle (migration 00005). Decouples "please
	// docker stop this container" from "docker rm it".
	//
	//   ContainerStopPending  TRUE when the platform wants the runner to
	//                         `docker stop` this container. Set by the
	//                         idle-stop reaper or admin stop-container.
	//   ContainerStoppedAt    recorded by the runner when it ACKs the
	//                         stop. Cleared on resume so a rewoken
	//                         session resets cleanly.
	//   RunningJobs           count of active docker exec's. The runner
	//                         increments/decrements; the idle-stop reaper
	//                         excludes rows with running_jobs > 0.
	ContainerStopPending bool
	ContainerStoppedAt   *time.Time
	RunningJobs          int32
}

// SessionTokenActive reports whether the session's identity token is
// usable. False when the session itself reached a terminal state or the
// token was explicitly revoked.
func (s *AgentSession) SessionTokenActive(now time.Time) bool {
	if s.SessionTokenRevokedAt != nil {
		return false
	}
	if s.Status.Terminal() {
		return false
	}
	return true
}

// SessionFilter is the optional-filter bag for ListRecentSessions. Each
// field is nil-means-no-constraint; the caller composes whichever set
// applies. Used by the admin global audit handler.
type SessionFilter struct {
	RoleKey *string
	Status  *string
	RepoID  *int64
	Since   *time.Time
}

// SessionPage carries offset/limit for ListRecentSessions paging. Kept
// separate from SessionFilter so the WHERE-side knobs and the windowing
// knobs don't fight in callers that only need one. limit <= 0 falls back
// to a server-side default; offset < 0 is treated as 0.
type SessionPage struct {
	Offset int
	Limit  int
}

// MessageKind is the discriminator on agent_session_messages.kind. The set
// mirrors the agent's IPC outbound shapes plus platform-side event /
// system records the agent itself never emits.
type MessageKind string

const (
	MessageKindEvent    MessageKind = "event"
	MessageKindMessage  MessageKind = "message"
	MessageKindToolCall MessageKind = "tool_call"
	MessageKindStatus   MessageKind = "status"
	MessageKindLog      MessageKind = "log"
	MessageKindDone     MessageKind = "done"
	MessageKindSystem   MessageKind = "system"
)

func (k MessageKind) Valid() bool {
	switch k {
	case MessageKindEvent, MessageKindMessage, MessageKindToolCall,
		MessageKindStatus, MessageKindLog, MessageKindDone, MessageKindSystem:
		return true
	}
	return false
}

// Message is one row of agent_session_messages.
//
// Payload is a JSON-encoded blob carrying kind-specific fields the columns
// above can't capture cleanly (tool_call.args/result, status.phase,
// log.level, message.tool_calls). Storing JSON keeps the schema stable
// across future agent shape changes without a migration.
type Message struct {
	ID         int64
	SessionID  int64
	Seq        int32
	Kind       MessageKind
	Role       string
	Content    string
	EventName  string
	ToolCallID string
	ToolName   string
	Payload    []byte // raw JSON, empty == "{}"
	CreatedAt  time.Time
}

// SessionInput is one row of agent_session_inputs — an event frame the
// runner will write to the agent's stdin. The platform enqueues these
// (cause events only — the seed `kind:history` frame the agent reads
// first is served separately via GET /sessions/{id}/history); the
// runner long-polls /inputs, marks consumed_at, and writes the payload
// one line at a time.
type SessionInput struct {
	ID         int64
	SessionID  int64
	Seq        int32
	Payload    []byte
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// CreateRunnerInput is the parameter bag for Repo.CreateRunner. owner_user_id
// must be nil for platform runners and non-nil for user runners; the Postgres
// CHECK constraint duplicates this rule at the DB level.
type CreateRunnerInput struct {
	Name        string
	OwnerUserID *int64
	Visibility  Visibility
	ActorID     int64 // FK to actors(id); resolved by handler
}

// Validate enforces the platform/user visibility invariant: platform
// runners must have a NULL owner, user runners must have one. Repo is
// allowed to assume Validate ran first — the DB CHECK is a safety net,
// not the primary boundary.
func (in CreateRunnerInput) Validate() error {
	if !in.Visibility.Valid() {
		return ErrInvalidVisibility
	}
	if in.Visibility == VisibilityPlatform && in.OwnerUserID != nil {
		return ErrPlatformRunnerHasOwner
	}
	if in.Visibility == VisibilityUser && in.OwnerUserID == nil {
		return ErrUserRunnerMissingOwner
	}
	return nil
}

// CreateSessionInput is what the admin handler hands the repo when starting
// a test session. RunnerID pins the session to a specific runner row when
// non-nil; the session spawner can leave it nil for unpinned rows that any
// eligible runner will claim.
//
// SessionTokenPrefix / SessionTokenHash / SessionTokenSealed are all minted
// by the caller (it holds the cryptobox) before this call lands. The repo
// only stores them; it never sees the plaintext on its own.
//
// The snapshot fields (RepoSHA / RoleKey / Cause* / RoleConfig) carry zero
// defaults so the admin smoke path keeps working uninstrumented; the
// session-spawn orchestrator populates them on the real trigger path.
type CreateSessionInput struct {
	RunnerID           *int64
	RepoID             *int64
	IssueNumber        *int32
	Role               string
	Model              string
	AgentImage         string
	WorkingBranch      string
	BaseBranch         string
	HostAddendum       string
	Env                map[string]string
	SessionTokenPrefix string
	SessionTokenHash   string
	SessionTokenSealed string
	// CreatedByActorID is the FK to the actors table for the entity
	// that triggered this session. Required — set by the spawner after
	// resolving the actor via the actor module's Resolver.
	CreatedByActorID int64

	// Snapshot.
	RepoSHA    string
	RoleKey    string
	CauseKind  string
	CauseID    string
	RoleConfig []byte // resolved role config (JSON), nil → "{}"
}

// RedeemEnrollResult is what EnrollValidator.RedeemEnrollment returns:
// the updated runner row plus the freshly minted plaintext agent token
// (shown once, stored as bcrypt(secret)).
type RedeemEnrollResult struct {
	Runner              *Runner
	AgentTokenPlaintext string
	Capabilities        []byte
}

// NewAgentToken is the pre-minted agent token the service hands to
// Repo.RedeemEnrollment. Service generates prefix+secret and bcrypts the
// secret; Repo only writes the row inside the redemption transaction.
type NewAgentToken struct {
	Prefix string
	Hash   string
}

// NewEnrollToken is the pre-minted enrollment token the admin handler
// hands to Repo.CreateRunner. Same shape as NewAgentToken — secret
// minting and hashing happen in the service layer; Repo just stores
// the (prefix, hash) pair.
type NewEnrollToken struct {
	Prefix string
	Hash   string
}

// NewSessionToken is the pre-minted session token Repo.ResumeSession
// installs on a rewoken row. Service mints (plaintext, prefix, hash,
// sealed) using cryptobox; infra only stores. The plaintext does not
// appear here — the runner gets it later via ClaimNextSession.
type NewSessionToken struct {
	Prefix string
	Hash   string
	Sealed string
}

// ContainerCleanupTask is one (session, container) pair the runner's
// cleanup sweeper should `docker rm`. Returned by
// Repo.ListPendingContainerCleanups and consumed by the runner-facing
// HTTP layer's `/api/runner/cleanup-tasks` endpoint.
type ContainerCleanupTask struct {
	SessionID   int64
	ContainerID string
}

// ContainerStopTask is one (session, container) pair the runner's
// stop sweeper should `docker stop`. Returned by
// Repo.ListPendingContainerStops and consumed by the runner-facing
// HTTP layer's `/api/runner/stop-tasks` endpoint.
type ContainerStopTask struct {
	SessionID   int64
	ContainerID string
}

// Errors.
var (
	ErrRunnerNotFound         = errors.New("runner not found")
	ErrRunnerConflict         = errors.New("runner name already taken for owner")
	ErrRunnerDisabled         = errors.New("runner disabled")
	ErrEnrollUsed             = errors.New("enrollment token already redeemed")
	ErrInvalidToken           = errors.New("invalid runner token")
	ErrTokenInactive          = errors.New("runner token inactive")
	ErrSessionNotFound        = errors.New("agent session not found")
	ErrSessionStateInvalid    = errors.New("agent session in unexpected state")
	ErrNoPendingSession       = errors.New("no pending session for runner")
	ErrInvalidSessionToken    = errors.New("invalid session token")
	ErrSessionTokenInactive   = errors.New("session token revoked or session terminal")
	ErrInvalidVisibility      = errors.New("invalid runner visibility")
	ErrPlatformRunnerHasOwner = errors.New("platform runner must not have an owner")
	ErrUserRunnerMissingOwner = errors.New("user runner must have an owner")
)

// Repo is the persistence abstraction. The Postgres impl satisfies it
// plus EnrollValidator (RedeemEnrollment is a stateful redemption, not
// just a lookup — it lives with persistence because it owns the
// transaction). The stateless validators (AgentValidator,
// SessionTokenValidator) are NOT on this interface; they live in
// modules/runner/service and compose Repo + bcrypt.
type Repo interface {
	// runners
	//
	// CreateRunner inserts a row with a pre-minted enrollment token.
	// Service mints (prefix, hash); Repo only stores. Invariants on
	// `in` must have been checked via CreateRunnerInput.Validate().
	CreateRunner(ctx context.Context, in CreateRunnerInput, enroll NewEnrollToken) (*Runner, error)
	GetRunnerByID(ctx context.Context, id int64) (*Runner, error)
	GetRunnerByAgentTokenPrefix(ctx context.Context, prefix string) (*Runner, error)
	ListRunners(ctx context.Context, ownerUserID *int64, visibility *Visibility) ([]*Runner, error)
	DisableRunner(ctx context.Context, id int64) error
	// DeleteRunner is a hard delete — removes the row from `runners`.
	// agent_sessions.runner_id is ON DELETE SET NULL, so historical
	// session rows survive but lose the runner pointer. Use this for
	// "remove from list" semantics; for "stop running but keep the
	// row" use DisableRunner.
	DeleteRunner(ctx context.Context, id int64) error
	UpdateRunnerHeartbeat(ctx context.Context, id int64, capabilities []byte) error

	// RedeemEnrollment runs the redemption transaction:
	//   1. SELECT FOR UPDATE the row matching enrollPrefix.
	//   2. Reject when status==disabled or enroll_token_used_at IS NOT NULL.
	//   3. Call verify(*Runner) — the service-supplied closure that bcrypt-
	//      compares the inbound secret against the locked row's hash.
	//      Returning an error aborts the transaction.
	//   4. UPDATE the row with the freshly minted agent token.
	//
	// The callback shape keeps bcrypt out of the persistence layer while
	// still letting the comparison happen under the row lock. Service
	// supplies prefix + the fresh agent token; infra owns the
	// transaction.
	RedeemEnrollment(
		ctx context.Context,
		enrollPrefix string,
		verify func(stored *Runner) error,
		newAgent NewAgentToken,
		capabilities []byte,
	) (*Runner, error)

	// sessions
	CreateSession(ctx context.Context, in CreateSessionInput) (*AgentSession, error)
	GetSessionByID(ctx context.Context, id int64) (*AgentSession, error)
	GetSessionByTokenPrefix(ctx context.Context, prefix string) (*AgentSession, error)
	ListSessions(ctx context.Context, runnerID *int64, status *SessionStatus, limit int) ([]*AgentSession, error)
	// ListSessionsByIssue returns every session row scoped to a (repo,
	// issue) tuple in spawn order. The agent_session module composes this
	// to (a) skip duplicate spawns on a role that already has a session
	// for an issue and (b) drive the audit query view.
	ListSessionsByIssue(ctx context.Context, repoID int64, issueNumber int32) ([]*AgentSession, error)
	// ListRecentSessions returns one page of the most-recent agent_sessions
	// across the platform, newest first, with optional filters. Powers the
	// global admin audit view (/api/admin/agent-sessions). Caller pairs this
	// with CountRecentSessions to drive the pager.
	ListRecentSessions(ctx context.Context, filter SessionFilter, page SessionPage) ([]*AgentSession, error)
	// CountRecentSessions returns the total rows matching the same filter
	// set as ListRecentSessions (windowing knobs ignored). Cheap COUNT(*)
	// on a partial index; safe to issue per page-load.
	CountRecentSessions(ctx context.Context, filter SessionFilter) (int64, error)
	ClaimNextSession(ctx context.Context, runnerID int64) (*AgentSession, error)
	MarkSessionRunning(ctx context.Context, id int64) error
	// MarkSessionTerminal flips a claimed/running session into a terminal
	// state (failed / succeeded / cancelled). session_token_sealed is
	// intentionally preserved so a rewake can re-export the same
	// HANGRIX_SESSION_TOKEN identity to the next container without
	// re-cloning or re-writing .git/config — see the query comment for
	// the full rationale. Inbound auth using the token is still blocked
	// while the row is terminal because SessionTokenActive() checks
	// Status.Terminal().
	MarkSessionTerminal(ctx context.Context, id int64, status SessionStatus, exitCode *int32, errMsg string) error
	// MarkSessionIdle flips a claimed/running session to 'idle'. Same
	// sealed-preservation contract as MarkSessionTerminal; differs only
	// in that the row is also still considered "logically alive" and
	// SessionTokenActive returns true. Used by the runner on clean
	// container exit when the parent issue is still open.
	MarkSessionIdle(ctx context.Context, id int64, exitCode *int32) error
	// ResumeSession flips an idle / failed / succeeded / cancelled row
	// back to 'pending' so a runner re-claims it. The caller chooses
	// the token to install: rewake-from-modern-row passes through the
	// existing prefix/hash/sealed unchanged (token identity preserved
	// across triggers); rewake-from-legacy-NULL'd-sealed mints a fresh
	// token. Refuses archived rows — the parent issue is the only
	// thing that can ever unstick an archived session, and "unsticking"
	// means "open a new issue".
	ResumeSession(ctx context.Context, id int64, newToken NewSessionToken) error
	// DeleteSession hard-deletes a session row + cascading message log
	// + inputs queue. The user-visible "trash" affordance: removes a
	// failed session the user is sure they don't need to inspect.
	DeleteSession(ctx context.Context, id int64) error
	// ArchiveSessionsByIssue flips every non-archived session on the
	// (repo, issue) tuple to 'archived'. Driven by issue.closed /
	// issue.merged — there is no per-session manual archive surface.
	// Returns the number of rows updated so the caller can log/no-op
	// when the issue had no live sessions.
	//
	// As of migration 00004, this also flips
	// container_cleanup_pending = TRUE for any archived row that owns a
	// live container, so the runner's cleanup sweeper picks them up.
	ArchiveSessionsByIssue(ctx context.Context, repoID int64, issueNumber int32) (int64, error)

	// ArchiveSessionByID archives one session by id and flags any live
	// container for runner cleanup. Used by the user-delete path when the
	// row has a container — hard-DELETE would orphan the container, so we
	// archive instead and rely on the cleanup sweeper to reach it.
	// Idempotent on already-archived rows.
	ArchiveSessionByID(ctx context.Context, id int64) error

	// SetSessionContainer records the long-lived container id the runner
	// is using for this session and bumps container_last_used_at. Called
	// from the runner-facing handler once per agent run, immediately
	// after orchestrator.Start. Idempotent — writing the same id again
	// just re-stamps the timestamp (which the 7-day idle reaper relies
	// on).
	SetSessionContainer(ctx context.Context, sessionID int64, containerID string) error

	// PingSession bumps container_last_used_at to NOW() without changing
	// any other column. The runtime calls this on every agent interaction
	// (tool call, thinking, output) so that roster_list's
	// last_activity_at reflects real-time liveness. Returns
	// ErrSessionNotFound when the row does not exist.
	PingSession(ctx context.Context, sessionID int64) error

	// FlagSessionContainerCleanup marks one session's container for
	// runner-side reaping. Used by the per-session delete path; the
	// archive and idle-sweep paths use their own batch queries.
	// Returns ErrSessionNotFound when no row matches; a no-op (return
	// nil) when the row exists but has no live container.
	FlagSessionContainerCleanup(ctx context.Context, sessionID int64) error

	// ListPendingContainerCleanups returns up to `limit` (session_id,
	// container_id) tuples that the given runner owns and the platform
	// has flagged for cleanup. The runner's cleanup sweeper polls this
	// and `docker rm`s each container.
	ListPendingContainerCleanups(ctx context.Context, runnerID int64, limit int) ([]ContainerCleanupTask, error)

	// ClearSessionContainer is the runner's ACK that `docker rm` of
	// (sessionID, ownerRunnerID)'s container succeeded. Clears
	// container_id, container_cleanup_pending, and container_last_used_at
	// in a single UPDATE. Scoped by runner_id so a misrouted ACK can't
	// clear a sibling runner's column.
	ClearSessionContainer(ctx context.Context, sessionID, ownerRunnerID int64) error

	// ---- container stop lifecycle (migration 00005) ----

	// FlagSessionContainerStop marks one session's container as needing
	// a `docker stop`. Set by the idle-stop reaper or admin stop-container
	// action. No-op when the row has no live container.
	FlagSessionContainerStop(ctx context.Context, sessionID int64) error

	// ListPendingContainerStops returns up to `limit` (session_id,
	// container_id) tuples that the given runner owns and the platform
	// has flagged for stop. The runner's stop sweeper polls this and
	// `docker stop`s each container.
	ListPendingContainerStops(ctx context.Context, runnerID int64, limit int) ([]ContainerStopTask, error)

	// AckContainerStop is the runner's ACK that `docker stop` of
	// (sessionID, ownerRunnerID)'s container succeeded. Sets
	// container_stopped_at = NOW(), clears container_stop_pending.
	// Scoped by runner_id so a misrouted ACK can't clear a sibling
	// runner's column.
	AckContainerStop(ctx context.Context, sessionID, ownerRunnerID int64) error

	// SweepIdleSessionContainersForStop is the idle-stop reaper: flags
	// container_stop_pending for every live container whose session has
	// been idle longer than `threshold`. Excludes rows with
	// running_jobs > 0 (mid-flight agent turn) and rows already flagged
	// for cleanup (container_cleanup_pending = TRUE). Returns the number
	// of rows flagged.
	SweepIdleSessionContainersForStop(ctx context.Context, threshold time.Duration) (int64, error)

	// SweepIdleSessionContainers is the idle reaper: flags every
	// live container whose session is non-running and hasn't been used
	// within `threshold`. Returns the number of rows flagged so the
	// reaper can log what it did. Called by the platform's reaper
	// goroutine on a 1-hour ticker.
	SweepIdleSessionContainers(ctx context.Context, threshold time.Duration) (int64, error)

	// SweepAbandonedSessionContainers is the giveup sweep: clears
	// container_id on rows that have been flagged for cleanup for longer
	// than `threshold` with no runner pickup (typically because the
	// owning runner is permanently offline). Returns row count for
	// logging.
	SweepAbandonedSessionContainers(ctx context.Context, threshold time.Duration) (int64, error)

	// messages
	AppendMessage(ctx context.Context, m *Message) (*Message, error)
	ListMessages(ctx context.Context, sessionID int64) ([]*Message, error)

	// inputs queue
	EnqueueInput(ctx context.Context, sessionID int64, payload []byte) (*SessionInput, error)
	ClaimPendingInputs(ctx context.Context, sessionID int64, limit int) ([]*SessionInput, error)
}

// EnrollValidator is the service-layer entry point the runner-facing
// handler holds: takes a plaintext `hgxe_<...>` token, runs the
// regex+parse+bcrypt+mint+UPDATE sequence (orchestrated in service,
// transaction in infra), and returns the redeemed agent token. The
// narrow interface keeps the handler free of Repo's wider surface.
type EnrollValidator interface {
	RedeemEnrollment(ctx context.Context, plaintext string, capabilities []byte) (*RedeemEnrollResult, error)
}

// AgentValidator resolves the `hgxr_<...>` plaintext to its runner row.
// The runner-facing handler's Bearer middleware holds this. The
// implementation in modules/runner/service composes Repo lookups with
// bcrypt — Repo never sees plaintext, the service never persists.
type AgentValidator interface {
	ValidateAgentToken(ctx context.Context, plaintext string) (*Runner, error)
}

// SessionTokenValidator resolves an `hgxs_<...>` plaintext to the
// agent_sessions row it belongs to. It is the auth entry point for every
// agent-facing platform surface (llm_proxy now, mcp_server later, host-
// repo push helper after that). Returning *AgentSession (rather than a
// narrow Validated wrapper) lets each consumer enforce its own per-row
// policy (repo allow-list, model allow-list, …) without round-tripping
// through this package. Implementation lives in modules/runner/service.
type SessionTokenValidator interface {
	ValidateSessionToken(ctx context.Context, plaintext string) (*AgentSession, error)
}

// Wire-format constants — distinct prefixes per spec so a header alone
// tells the auth router which validator to invoke.
const (
	EnrollTokenWirePrefix  = "hgxe_"
	AgentTokenWirePrefix   = "hgxr_"
	SessionTokenWirePrefix = "hgxs_"
	TokenPrefixLen         = 8
	TokenSecretLen         = 32
)
