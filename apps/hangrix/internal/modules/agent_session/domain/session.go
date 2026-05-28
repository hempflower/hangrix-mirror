// Package domain declares the agent_session module's external contract:
// the orchestration layer that sits on top of runner.domain.Repo and turns
// host-repo issue events into per-role session rows. Persistence stays in
// the runner module (one agent_sessions table, owned by runner/infra); this
// package only adds the lifecycle semantics on top.
//
// Three interfaces, each consumed by a different caller:
//
//   - Spawner — the issue module calls it on issue.opened (and other
//     triggers like comment-mention / push / review vote) so the per-role
//     sessions wake on their own. The spawner reads `.hangrix/agents.yml`
//     at the host base-branch HEAD and writes a session row pinned to
//     that commit.
//
//   - Archiver — the issue module calls it on issue.closed /
//     issue.merged. Per spec there is no per-session manual archive; the
//     parent issue is the sole archiver.
//
//   - Auditor — the admin / agents-tab UI calls ListByIssue + GetSession
//
//   - ListMessages to reconstruct the (repo_sha, cause, role_config)
//     audit chain plus the per-session message log.
package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/pkg/actor"
)

// CauseKind classifies the upstream event that spawned a session. It maps
// onto agent_sessions.cause_kind (TEXT, free-form so future trigger
// classes can extend without a migration). Values stay short slug-style:
// audit consumers branch on them.
type CauseKind string

const (
	// CauseKindIssueOpened — a brand new issue was created. The
	// dispatcher / triage roles whose triggers include
	// agentsconfig.TriggerIssueOpened wake here.
	CauseKindIssueOpened CauseKind = "issue_opened"

	// CauseKindCommentMentioned — a comment @-mentioned the role.
	CauseKindCommentMentioned CauseKind = "comment_mentioned"

	// CauseKindCommitPushed — a push landed on the issue branch.
	CauseKindCommitPushed CauseKind = "commit_pushed"

	// CauseKindReviewVote — a reviewer voted approve / reject.
	CauseKindReviewVote CauseKind = "review_vote"

	// CauseKindPatchSubmitted — an agent submitted a patch to the issue.
	CauseKindPatchSubmitted CauseKind = "patch_submitted"

	// CauseKindPatchApplyRequested — a maintainer triggered application
	// of a patch submission; the apply agent is waking to `git am` it.
	CauseKindPatchApplyRequested CauseKind = "patch_apply_requested"

	// CauseKindManual — admin spawn path (smoke / debug tooling).
	CauseKindManual CauseKind = "manual"

	// CauseKindQuestionnaireAnswered — a user submitted an answer to a
	// questionnaire the agent issued via ask_question. The issuing agent
	// is woken automatically via Spawner.OnTrigger with a targeted
	// RoleKey — no trigger config needed.
	CauseKindQuestionnaireAnswered CauseKind = "questionnaire_answered"

	// CauseKindCIStatusChanged — a CI workflow run's status changed
	// (e.g. pending → running → success/failure).
	CauseKindCIStatusChanged CauseKind = "ci_status_changed"
)

// TriggerInput is the payload Spawner.OnTrigger consumes. It carries
// enough context to (a) match roles whose triggers include the named
// event, (b) populate the session's snapshot columns, and (c) seed the
// agent's inputs queue with the cause event.
//
// One TriggerInput fans out to N session rows — one per role on the host
// yaml whose triggers list includes Trigger. Mention routing is open
// (any commenter can wake any role); the spawn loop does not consult
// any per-role actor gate.
type TriggerInput struct {
	// Trigger is the agentsconfig event the spawn should match against
	// each role's `triggers:` list. A role with no matching trigger is
	// skipped (no session row created).
	Trigger agentsconfig.Trigger

	// CauseKind is what gets persisted onto the new session's
	// cause_kind column. Pairs with CauseID for the audit chain.
	CauseKind CauseKind

	// CauseID is the opaque upstream-artefact id (e.g. comment id, sha,
	// review vote id). Empty for issue.opened. Stored on the row so
	// audit consumers can join back to the originating record.
	CauseID string

	// RepoID identifies the host repo.
	RepoID int64

	// IssueNumber identifies the per-repo issue. Negative or zero
	// values are rejected by the spawner — every spawn is scoped to an
	// issue.
	IssueNumber int32

	// Actor is the resolved actor.Ref for the entity that triggered
	// this event. Set by callers via the actor module's Resolver.
	// When non-nil, the spawner uses it directly; when nil (legacy
	// callers), the spawner falls back to ActorID below.
	//
	// Supersedes ActorID — new callers should populate Actor instead.
	Actor *actor.Ref

	// Deprecated: ActorID is the user who triggered the event (issue
	// author for issue_opened, commenter for comment_mentioned).
	// Stored as the session row's `created_by`.
	//
	// New callers should set Actor instead; the spawner will use
	// Actor when non-nil and fall back to ActorID otherwise. This
	// field is kept for backward compatibility while callers
	// (issue handler, platform_api) migrate.
	//
	// Mention_by gating is NOT applied here — the issue handler is
	// the authority on the actor↔repo relationship and pre-filters
	// before invoking OnTrigger. The spawner trusts the caller's
	// matched-role set.
	ActorID int64

	// RoleKey, when non-empty, restricts the trigger fan-out to a
	// single role. Reserved for callers that want to wake exactly one
	// role (admin smoke tools, retry flows). The normal comment / push
	// paths leave it empty and let each role's TriggerSpec filter
	// decide whether to fire.
	RoleKey string

	// Comment is the author + mention context for Trigger ==
	// issue.comment. nil for any other trigger. Populated by the
	// issue handler so the spawner can evaluate per-role
	// CommentFilter (mentioned_only / from_roles / from_users).
	Comment *CommentContext

	// ChangedPaths is the list of files affected by a push, used by
	// the spawner to evaluate per-role PushFilter (paths /
	// paths_ignore) for Trigger == commit.pushed. nil / empty for
	// other triggers.
	ChangedPaths []string

	// Payload, when non-empty, is merged into the agent's input event
	// frame as `payload:`. Callers use this to attach trigger-specific
	// data (comment id + body, push old/new sha, review vote ↔ value)
	// the agent needs to act on the event. Must be valid JSON object
	// bytes (empty `{}` accepted); the spawner re-marshals to embed.
	Payload []byte
}

// CommentContext is the author + mention context the issue handler
// attaches when firing TriggerIssueComment. The spawner consults it to
// evaluate each subscribed role's CommentFilter — the mention path,
// the from-role gate, and the from-user gate all read from here.
type CommentContext struct {
	// AuthorRoleKey is the role key of the agent that posted the
	// comment, when the commenter is an agent. Empty when the
	// commenter is a human user. Drives the from_roles filter.
	AuthorRoleKey string

	// AuthorUser is the platform username of the commenter when the
	// commenter is a human user. Empty for agent-authored comments.
	// Drives the from_users filter.
	AuthorUser string

	// Mentions is the deduplicated list of `@agent-<role-key>` matches
	// parsed from the comment body, with the `agent-` prefix stripped.
	// Drives the mentioned_only filter — a role with mentioned_only =
	// true fires only when its own key is in this list.
	Mentions []string
}

// SpawnedSession is the trimmed view Spawner returns. Full row + history
// still live behind runner.domain.Repo; this is just the handles a caller
// needs to log "session X for role Y started".
type SpawnedSession struct {
	SessionID int64
	RoleKey   string
	RunnerID  *int64 // nil when no runner is pinned at spawn time

	// Action records what the spawner did for this role. "spawned" for
	// a fresh session row; "enqueued" for an additional event frame on
	// a pre-existing live session; "rewoken" for a terminal-but-not-
	// archived session that was flipped back to pending. Audit /
	// logging callers branch on this; tests assert on it.
	Action SpawnAction
}

// SpawnAction is the discrete outcome of one role match. Returned from
// Spawner.OnTrigger so callers can log "x roles spawned, y enqueued"
// without inspecting session-state changes.
type SpawnAction string

const (
	// SpawnActionSpawned — a fresh agent_sessions row was created and
	// the inputs queue seeded with (history, cause) frames.
	SpawnActionSpawned SpawnAction = "spawned"
	// SpawnActionEnqueued — an existing non-terminal session row was
	// reused and the cause event was appended to its inputs queue. No
	// new container starts; the agent receives the event over its
	// long-poll /inputs stream.
	SpawnActionEnqueued SpawnAction = "enqueued"
	// SpawnActionRewoken — an existing terminal-but-not-archived
	// session was flipped back to pending so a runner re-claims it,
	// and the cause event was appended to its inputs queue.
	SpawnActionRewoken SpawnAction = "rewoken"
)

// Spawner translates upstream events into agent_sessions rows. The
// implementation in service/spawner.go is pure-Go orchestration; it does
// not own state of its own. Composes:
//
//   - agentsconfig parser   (host yaml)
//   - HostBlobReader        (read files at <ref>:<path> from a bare repo)
//   - repo.Store            (locate the host repo)
//   - runner.Repo           (persist the session + history seed)
//   - RunnerPicker          (pick a runner_id by visibility + capacity)
//
// All errors flow upward; the issue handler logs and continues so a yaml
// hiccup doesn't break issue creation. Spawning is best-effort by design
// — operators repair the host yaml then nudge the issue, no transactional
// coupling.
type Spawner interface {
	// OnTrigger fans an upstream event out to every role on the host
	// yaml that subscribes to in.Trigger. For each match:
	//
	//   - If TriggerInput.RoleKey is set, only that single role
	//     participates (used by the mention path to scope `@agent-X`).
	//   - If a previously-spawned session for the (issue, role) has
	//     the exact (cause_kind, cause_id) tuple already → no-op.
	//     This is what makes re-firing `issue.opened` on the same
	//     issue idempotent across retries.
	//   - Else if a live (pending / claimed / running) session row
	//     exists for the role → append the event onto its inputs
	//     queue. The agent (or the runner that will claim it) sees
	//     the event on its next stdin read.
	//   - Else if the role's most recent session is archived → spawn
	//     a fresh row that replaces the archived predecessor. The
	//     archived row stays on disk for audit; the new row is the
	//     canonical session for the (issue, role) going forward.
	//   - Else → spawn a fresh row (row + history seed path).
	//
	// Returns one SpawnedSession per role that was either spawned or
	// enqueued; rows that were skipped (no match / archived / exact
	// dupe) are not present in the result.
	OnTrigger(ctx context.Context, in TriggerInput) ([]SpawnedSession, error)

	// LoadHostConfig parses `.hangrix/agents.yml` at the host repo's
	// default-branch tip. Returns (nil, nil) when the file is absent
	// (non-agent host — the common case). Surfaces ErrHostConfigInvalid
	// for parse failures so the caller can log and continue.
	//
	// Exposed so the issue handler can resolve `@agent-<role-key>`
	// mentions against the role declarations before invoking
	// OnTrigger — anything that doesn't match a declared role is
	// silently dropped. Re-resolving the host yaml each call is fine:
	// the file is small, the operation is read-only (just a `git
	// cat-file -p`), and a memoising layer would have to invalidate
	// on every push so it isn't worth the complication.
	LoadHostConfig(ctx context.Context, repoID int64) (*agentsconfig.HostConfig, error)
}

// Archiver flips every non-archived session on a (repo, issue) to
// archived. The issue module calls this on close / merge — the parent
// issue is the only thing that can archive sessions (docs/agent-config.md
// §"Session 模型 → 归档只能由 issue.closed / issue.merged 触发").
type Archiver interface {
	// OnIssueClosed flips every non-archived session on the issue.
	// Returns the number of rows that flipped — callers can log or
	// no-op when there were no live sessions.
	OnIssueClosed(ctx context.Context, repoID int64, issueNumber int32) (int64, error)
}

// Controller exposes the per-session user-driven lifecycle actions —
// Stop (cancel a running container), Resume (rewake a failed/idle row
// so the runner picks it up again), Delete (remove the row entirely).
// The issue handler exposes these on the public agent-sessions route;
// admin tooling can layer onto the same interface later.
type Controller interface {
	// Stop signals the agent to shut down and marks the session
	// 'failed'. Enqueues a control:shutdown frame onto the inputs
	// queue so a running container exits cleanly when it next polls;
	// the failed status is what the UI surfaces and what unblocks the
	// resume button. Returns ErrSessionNotFound for unknown ids and
	// nil if the session was already terminal (idempotent).
	Stop(ctx context.Context, sessionID int64, reason string) error

	// Resume flips an idle / failed / succeeded row back to 'pending'
	// and re-mints its session token. Returns ErrSessionNotFound for
	// unknown ids and ErrNotResumable when the row is archived /
	// already pending. Used by the web UI resume button; enqueues a
	// manual.resume event.
	Resume(ctx context.Context, sessionID int64) error

	// Recover is the agent-initiated counterpart of Resume. Same token
	// mint and session flip, but enqueues a manual.recover event whose
	// payload carries the caller's role key (recovered_by) so the
	// resumed agent can distinguish agent-initiated recovery from a
	// user clicking the resume button in the web UI.
	Recover(ctx context.Context, sessionID int64, recoveredBy string) error

	// Delete hard-removes the session row (and cascades the message
	// log + inputs queue). Refuses live sessions — the user must Stop
	// first so the running container can't keep emitting messages
	// onto a row that no longer exists.
	Delete(ctx context.Context, sessionID int64) error

	// StopContainerNow flags the session's container for an immediate
	// docker stop by the owning runner. The runner picks this up on
	// its next stop-tasks poll, `docker stop`s the container, and
	// ACKs. Administrators use this to manually stop a stuck or
	// idle container without terminating the session row —
	// the session stays in its current status and can be resumed
	// later.
	StopContainerNow(ctx context.Context, sessionID int64) error

	// RemoveContainerNow flags the session's container for an
	// immediate docker rm by the owning runner. The runner picks this
	// up on its next cleanup-tasks poll. Use this when the container
	// is already stopped (or StopContainerNow was called first) and
	// the admin wants to free the runner's disk space immediately
	// rather than wait for the idle removal sweep.
	RemoveContainerNow(ctx context.Context, sessionID int64) error
}

// ErrNotResumable is returned by Controller.Resume when a session row
// cannot be flipped back to pending (archived, or already live).
var ErrNotResumable = errors.New("session not resumable")

// ErrSessionLive is returned by Controller.Delete when the caller tries
// to remove a session that's still pending/claimed/running. Stop first.
var ErrSessionLive = errors.New("session is still live; stop it first")

// AuditSession is one row of the cross-session query view. Contains the
// snapshot pin (`repo_sha`) so a consumer can re-checkout exactly the
// host state that produced any commit the session made.
//
// Failure fields (ExitCode / ErrorMessage) are populated by the runner
// via MarkSessionTerminal when a session ends in a non-success status.
// They give the audit consumer a one-line "why did this fail" hook
// without having to fetch the full message log.
//
// Container fields are populated from the agent_sessions row so the
// admin UI can render container lifecycle state inline.
type AuditSession struct {
	SessionID    int64
	RunnerID     *int64
	RepoID       int64
	Issue        int32
	RoleKey      string
	Status       string
	RepoSHA      string
	CauseKind    string
	CauseID      string
	RoleConfig   json.RawMessage
	ExitCode     *int32
	ErrorMessage string
	CreatedAt    time.Time
	EndedAt      *time.Time

	// Container lifecycle (migration 00004 + 00005).
	ContainerID             string
	ContainerLastUsedAt     *time.Time
	ContainerStoppedAt      *time.Time
	ContainerStopPending    bool
	ContainerCleanupPending bool
	RunningJobs             int32
}

// SessionMessage is one frame of a session's message log. Agents emit
// these on stdout (kind=message / tool_call / status / log / done) and
// the platform appends synthetic frames (kind=event for the trigger,
// kind=system for orchestration errors). Payload carries kind-specific
// JSON the columns can't represent cleanly (e.g. tool_call.args /
// result, message.tool_calls).
type SessionMessage struct {
	ID         int64
	Seq        int32
	Kind       string
	Role       string
	Content    string
	EventName  string
	ToolCallID string
	ToolName   string
	Payload    json.RawMessage
	CreatedAt  time.Time
}

// RecentFilter is the optional-filter bag for Auditor.ListRecent. Each
// pointer field is nil-means-no-constraint. Limit <= 0 falls back to a
// server-side default; Offset < 0 is treated as 0. Limit/Offset window the
// result; the matching unbounded total comes back alongside the page so the
// admin UI can render a pager.
type RecentFilter struct {
	RoleKey *string
	Status  *string
	RepoID  *int64
	Since   *time.Time
	Limit   int
	Offset  int
}

// Auditor exposes the agent-session audit view. Mounted under the issue
// route (`/api/repos/{owner}/{name}/issues/{n}/agent-sessions`) for
// non-admin readers, and under `/api/admin/agent-sessions` for the
// admin audit pages.
type Auditor interface {
	// ListByIssue returns every session ever spawned on the (repo,
	// issue) in spawn order. Includes already-archived rows — the
	// audit view is append-only.
	ListByIssue(ctx context.Context, repoID int64, issueNumber int32) ([]AuditSession, error)

	// ListRecent returns one page of the most-recent sessions across the
	// platform (newest first), filtered by optional role / status / repo /
	// since, alongside the unbounded total matching the same filter set so
	// callers can render a pager. Powers the admin global audit view.
	ListRecent(ctx context.Context, opts RecentFilter) (rows []AuditSession, total int64, err error)

	// GetSession returns one session by id. Caller is responsible for
	// any (repo, issue) scoping — the method does not enforce it on
	// its own.
	GetSession(ctx context.Context, sessionID int64) (*AuditSession, error)

	// ListMessages returns every message frame for a session, ordered
	// by seq ascending. Empty slice for a session that hasn't yet
	// produced any output. Returns ErrSessionNotFound if the session
	// itself doesn't exist.
	ListMessages(ctx context.Context, sessionID int64) ([]SessionMessage, error)
}

// ErrSessionNotFound is returned when a session lookup misses.
var ErrSessionNotFound = errors.New("agent session not found")

// HostBlobReader resolves <ref>:<path> in a bare repo to bytes. The repo
// module already implements this via `git cat-file -p`; abstracting it
// here lets the spawner be unit-testable without a real git binary. The
// concrete impl lives in service/host_blob.go (a tiny wrapper around the
// repo module's existing helper).
type HostBlobReader interface {
	// ReadBlob returns the bytes of <ref>:<path>. (nil, false) when
	// the blob is missing — callers treat "no .hangrix/agents.yml" as
	// "no roles", not as an error.
	ReadBlob(ctx context.Context, repoFsPath, ref, path string) ([]byte, bool)

	// ListBlobs returns the repo-relative paths of the entries directly
	// under <ref>:<dir>. (nil, false) when the directory is missing.
	// Used to enumerate `.hangrix/agents/*.md` role files.
	ListBlobs(ctx context.Context, repoFsPath, ref, dir string) ([]string, bool)
}

// Sentinel errors. Spawner propagates these so the issue handler can log
// gracefully without parsing strings.
var (
	// ErrNoHostConfig means the host repo has no `.hangrix/agents.yml`
	// — a non-agent host. Spawner returns zero sessions and a nil
	// error (the issue handler ignores it).
	ErrNoHostConfig = errors.New("host repo has no .hangrix/agents.yml")

	// ErrHostConfigInvalid wraps a parse failure on the host yaml.
	// Issue creation should NOT block on this — the operator repairs
	// the file then resyncs.
	ErrHostConfigInvalid = errors.New("host repo .hangrix/agents.yml is invalid")

	// ErrNoRunner means no eligible runner is currently active for
	// the host repo's visibility class. Spawner pins runner_id = NULL
	// in that case; the first eligible runner picks it up via
	// ClaimNextSession. The error is only returned by RunnerPicker
	// implementations that strictly require a pin at spawn time —
	// the default policy uses unpinned-OK and never raises it.
	ErrNoRunner = errors.New("no eligible runner")
)
