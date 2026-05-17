// Package domain declares the agent_session module's external contract:
// the orchestration layer that sits on top of runner.domain.Repo and turns
// host-repo issue events into per-role session rows. Persistence stays in
// the runner module (one agent_sessions table, owned by runner/infra); this
// package only adds the M7a lifecycle semantics on top.
//
// Three interfaces, each consumed by a different caller:
//
//   - Spawner — the issue module calls it on issue.opened (and, in M7b,
//     other triggers) so the per-role sessions wake on their own. The
//     spawner reads `.hangrix/agents.yml` at the host base-branch HEAD,
//     resolves each role's agent ref to a sha, and writes a session row
//     with the M7a snapshot columns populated.
//
//   - Archiver — the issue module calls it on issue.closed /
//     issue.merged. Per spec there is no per-session manual archive; the
//     parent issue is the sole archiver.
//
//   - Auditor — the admin / future swim-lane UI calls ListByIssue to
//     reconstruct the (agent_sha, repo_sha, cause) audit chain.
package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
)

// CauseKind classifies the upstream event that spawned a session. It maps
// onto agent_sessions.cause_kind (TEXT, free-form so M7b can extend
// without a migration). Values stay short slug-style: audit consumers
// branch on them.
type CauseKind string

const (
	// CauseKindIssueOpened — a brand new issue was created. The
	// dispatcher / triage roles whose triggers include
	// agentsconfig.TriggerIssueOpened wake here.
	CauseKindIssueOpened CauseKind = "issue_opened"

	// CauseKindCommentMentioned — M7b: a comment @-mentioned the role.
	// Defined here so cause-kind values stay in one place even though
	// M7a doesn't yet emit them.
	CauseKindCommentMentioned CauseKind = "comment_mentioned"

	// CauseKindCommitPushed — M7b: a push landed on the issue branch.
	CauseKindCommitPushed CauseKind = "commit_pushed"

	// CauseKindReviewVote — M7b: a reviewer voted approve / reject.
	CauseKindReviewVote CauseKind = "review_vote"

	// CauseKindManual — admin spawn path (M6c smoke / debug tooling).
	CauseKindManual CauseKind = "manual"
)

// TriggerInput is the payload Spawner.OnTrigger consumes. It carries
// enough context to (a) match roles whose triggers include the named
// event, (b) populate the session's snapshot columns, and (c) seed the
// agent's inputs queue with the cause event.
//
// One TriggerInput fans out to N session rows — one per role on the host
// yaml whose triggers list includes Trigger and whose `mention_by` (when
// the cause is a mention) accepts the actor. M7a only emits
// TriggerIssueOpened; M7b adds the rest.
type TriggerInput struct {
	// Trigger is the agentsconfig event the spawn should match against
	// each role's `triggers:` list. A role with no matching trigger is
	// skipped (no session row created).
	Trigger agentsconfig.Trigger

	// CauseKind is what gets persisted onto the new session's
	// cause_kind column. Pairs with CauseID for the M7a audit chain.
	CauseKind CauseKind

	// CauseID is the opaque upstream-artefact id (e.g. comment id, sha,
	// review vote id). M7a: empty for issue.opened. Stored on the row
	// so audit consumers can join back to the originating record.
	CauseID string

	// RepoID identifies the host repo.
	RepoID int64

	// IssueNumber identifies the per-repo issue. Negative or zero
	// values are rejected by the spawner — every spawn is scoped to an
	// issue.
	IssueNumber int32

	// ActorID is the user who triggered the event (issue author for
	// issue_opened, commenter for comment_mentioned). Stored as the
	// session row's `created_by`.
	//
	// Mention_by gating is NOT applied here — the issue handler is
	// the authority on the actor↔repo relationship and pre-filters
	// before invoking OnTrigger. The spawner trusts the caller's
	// matched-role set.
	ActorID int64

	// RoleKey, when non-empty, restricts the trigger fan-out to a
	// single role. Used by the mention path so `@agent-backend` only
	// wakes the backend role — not every role whose triggers include
	// issue.comment.mentioned. Empty = match all roles whose triggers
	// list the event.
	RoleKey string

	// Payload, when non-empty, is merged into the agent's input event
	// frame as `payload:`. Callers use this to attach trigger-specific
	// data (comment id + body, push old/new sha, review vote ↔ value)
	// the agent needs to act on the event. Must be valid JSON object
	// bytes (empty `{}` accepted); the spawner re-marshals to embed.
	Payload []byte
}

// SpawnedSession is the trimmed view Spawner returns. Full row + history
// still live behind runner.domain.Repo; this is just the handles a caller
// needs to log "session X for role Y started".
type SpawnedSession struct {
	SessionID int64
	RoleKey   string
	AgentRepo string // "<owner>/<name>@<sha>" — the snapshot pin
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
//   - agentsconfig parser   (host yaml + lock file + agent manifest)
//   - HostBlobReader        (read files at <ref>:<path> from a bare repo)
//   - git.Git               (resolve agent ref → sha when no lock entry)
//   - repo.Store            (locate the host + agent repos, validate
//                            kind=agent on the agent repo)
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
	//   - Else if the role's most recent session is archived → skip
	//     the role. Issue is dead for it (M7a: parent issue is the
	//     only thing that can archive).
	//   - Else → spawn a fresh row (the M7a row + history seed path).
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
	// Exposed so the issue handler can apply the per-role `mention_by`
	// gate before invoking OnTrigger — the policy needs the role's
	// MentionBy + the caller's actor permissions, both of which live
	// outside the spawner's purview. Re-resolving the host yaml each
	// call is fine: the file is small, the operation is read-only
	// (just a `git cat-file -p`), and a memoising layer would have to
	// invalidate on every push so it isn't worth the complication.
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

// AuditSession is one row of the cross-session query view promised in
// roadmap.md §M7a Phase 2. Contains the snapshot pins so a consumer can
// re-checkout exactly the agent + host state that produced any commit
// the session made (`(agent_sha, repo_sha)` pair is the reproducibility
// anchor).
type AuditSession struct {
	SessionID  int64
	RunnerID   *int64
	RepoID     int64
	Issue      int32
	RoleKey    string
	Status     string
	AgentRepo  string // canonical "<owner>/<name>@<sha>" pin
	AgentSHA   string
	RepoSHA    string
	CauseKind  string
	CauseID    string
	RoleConfig json.RawMessage
	CreatedAt  time.Time
	EndedAt    *time.Time
}

// Auditor exposes the M7a audit chain as a queryable view. The admin
// handler mounts ListByIssue at /api/admin/issues/{repo_id}/{issue}/
// agent-sessions so an operator can verify "this commit was made by
// session S, role R, agent_sha A, repo_sha P, caused by comment C".
type Auditor interface {
	// ListByIssue returns every session ever spawned on the (repo,
	// issue) in spawn order. Includes already-archived rows — the
	// audit view is append-only.
	ListByIssue(ctx context.Context, repoID int64, issueNumber int32) ([]AuditSession, error)
}

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

	// ErrAgentRepoNotFound means the role's `agent: <owner>/<name>@<ref>`
	// points at a repo that doesn't exist on this platform (or isn't
	// kind=agent). The spawner aborts the per-role spawn but continues
	// with other roles.
	ErrAgentRepoNotFound = errors.New("agent repo not found")

	// ErrAgentRefUnresolved means neither the lock file nor the
	// agent repo's live refs resolved the role's ref to a sha. Same
	// per-role skip rule.
	ErrAgentRefUnresolved = errors.New("agent ref unresolved to a sha")

	// ErrNoRunner means no eligible runner is currently active for
	// the host repo's visibility class. Spawner pins runner_id = NULL
	// in that case (M7a accepts unpinned rows; the first eligible
	// runner picks it up via ClaimNextSession). The error is only
	// returned by RunnerPicker implementations that strictly require
	// a pin at spawn time — the default policy uses unpinned-OK and
	// never raises it.
	ErrNoRunner = errors.New("no eligible runner")
)
