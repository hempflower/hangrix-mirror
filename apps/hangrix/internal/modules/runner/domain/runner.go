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
//	                                   agent to call platform LLM, platform MCP,
//	                                   etc. NOT coupled to any LLM provider)
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
// stores it as-is for diagnostics; M6c does not parse it.
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
	CreatedBy           int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
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
//   pending → claimed → running                       — one container life.
//   running → succeeded | failed | cancelled          — that container ended.
//   running → idle                                    — the container finished
//             one turn but the parent issue is still open. The row stays put
//             waiting for the next trigger, which will recycle it back to
//             pending (and from there through claimed → running again).
//   * → archived                                      — the parent issue
//             closed / merged. The row is dead for good; restart means a new
//             session on a new issue.
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
// next trigger and goes back to pending). Succeeded/failed/cancelled are
// terminal in the M6c sense — they describe the most recent container,
// but the row itself can still get a fresh pending turn under M7a if the
// session is per-role-not-per-container; for now we keep the M6c semantics
// (terminal=row done) because the per-role lifecycle module is M7a Phase 2
// and the new idle / archived states are how the row signals "still
// active" vs. "permanently done" to that module.
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
//     inject HANGRIX_SESSION_TOKEN into the agent's env.
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
	// AgentRepo is the agent bundle the runner should materialise, as
	// "<owner>/<name>@<sha>" (sha is required, resolved from the host
	// yaml ref via the agents.lock file). The runner downloads the
	// corresponding tarball from /api/runner/agent-bundles/... and
	// mounts it read-only at /opt/hangrix/bundle. M6c's bundle_dir
	// (a runner-side filesystem path) has been retired in migration
	// 00002 — this field is what runners read now.
	AgentRepo             string
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
	CreatedAt             time.Time
	ClaimedAt             *time.Time
	StartedAt             *time.Time
	EndedAt               *time.Time

	// M7a snapshot fields. Frozen at session-spawn so a later audit can
	// reconstruct exactly which agent + host configuration produced any
	// commit made by this session. See docs/agent-config.md §"Session
	// 模型".
	AgentSHA   string          // agent repo commit sha pulled by runner
	RepoSHA    string          // host repo base-branch sha at spawn time
	RoleKey    string          // role identifier from host yaml; commit author for pushes
	CauseKind  string          // trigger family (e.g. "issue_opened", "comment_mentioned")
	CauseID    string          // upstream artefact id (opaque to runner)
	RoleConfig []byte          // resolved role config snapshot (JSON), empty == "{}"
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

// MessageKind is the discriminator on agent_session_messages.kind. The set
// mirrors the agent's IPC outbound shapes plus platform-side event /
// system records the agent itself never emits.
type MessageKind string

const (
	MessageKindEvent    MessageKind = "event"
	MessageKindMessage  MessageKind = "message"
	MessageKindToolCall MessageKind = "tool_call"
	MessageKindStatus   MessageKind = "status"
	MessageKindLog     MessageKind = "log"
	MessageKindDone    MessageKind = "done"
	MessageKindSystem  MessageKind = "system"
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

// SessionInput is one row of agent_session_inputs — a frame the runner
// will write to the agent's stdin. The platform enqueues these (the first
// is always a "history" frame seeded at session-start); the runner long-
// polls, marks consumed_at, and writes the payload one line at a time.
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
	CreatedBy   int64
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
// a test session. RunnerID pins the session to a specific runner row; M6c
// always pins, M7a's dispatcher will widen this.
//
// SessionTokenPrefix / SessionTokenHash / SessionTokenSealed are all minted
// by the caller (it holds the cryptobox) before this call lands. The repo
// only stores them; it never sees the plaintext on its own.
//
// The M7a snapshot fields (AgentSHA / RepoSHA / RoleKey / Cause* /
// RoleConfig) carry zero defaults so the M6c admin path keeps working
// uninstrumented. The M7a session-spawn orchestrator MUST populate them;
// audit consumers detect "M6c-era row" by an empty AgentSHA.
type CreateSessionInput struct {
	RunnerID           *int64
	RepoID             *int64
	IssueNumber        *int32
	Role               string
	Model              string
	AgentImage         string
	AgentRepo          string // "<owner>/<name>@<sha>"
	WorkingBranch      string
	BaseBranch         string
	HostAddendum       string
	Env                map[string]string
	SessionTokenPrefix string
	SessionTokenHash   string
	SessionTokenSealed string
	CreatedBy          int64

	// M7a snapshot.
	AgentSHA   string
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

// HistoryFrame is what BuildHistoryFrame returns — the seed `kind:history`
// payload the runner must feed into the agent's stdin as the first frame.
// JSON shape matches ipc.Inbound{Kind:"history", Messages: [...]}.
type HistoryFrame struct {
	Payload []byte // JSON-encoded {"kind":"history","messages":[...]}
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
	// for an issue and (b) drive the M7a audit query view.
	ListSessionsByIssue(ctx context.Context, repoID int64, issueNumber int32) ([]*AgentSession, error)
	ClaimNextSession(ctx context.Context, runnerID int64) (*AgentSession, error)
	MarkSessionRunning(ctx context.Context, id int64) error
	MarkSessionTerminal(ctx context.Context, id int64, status SessionStatus, exitCode *int32, errMsg string) error
	// ArchiveSessionsByIssue flips every non-archived session on the
	// (repo, issue) tuple to 'archived'. Driven by issue.closed /
	// issue.merged — there is no per-session manual archive surface.
	// Returns the number of rows updated so the caller can log/no-op
	// when the issue had no live sessions.
	ArchiveSessionsByIssue(ctx context.Context, repoID int64, issueNumber int32) (int64, error)

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
