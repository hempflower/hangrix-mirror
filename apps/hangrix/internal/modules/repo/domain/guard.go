package domain

import (
	"context"
	"errors"
)

// BranchWriteOp describes a single ref update attempt the guard is asked to
// authorize. Old/New SHA are zero-strings ("") for branch creation / deletion
// respectively; Force is true when the receive-pack negotiation flagged a
// non-fast-forward update.
type BranchWriteOp struct {
	RepoID     int64
	Branch     string
	OldSHA     string
	NewSHA     string
	Force      bool
	IsCreate   bool
	IsDelete   bool
	IsInternal bool // true for server-initiated merges; bypasses issue/base gates.
}

// BranchWriteGuard authorizes (or rejects) a branch write before the repo
// module performs it. Multiple guards may be registered through ioc; the
// handler iterates them in order and returns the first non-nil error.
//
// Guards exist to support M4: the issue module installs a guard that rejects
// pushes to branches that aren't `issue/<n>` and locks the default branch to
// merge-only. M3 ships with no guards installed — every write is allowed.
type BranchWriteGuard interface {
	CheckBranchWrite(ctx context.Context, op BranchWriteOp) error
}

// ErrBranchWriteDenied is the sentinel guards return to reject the operation.
// Handlers turn it into a 403 with the wrapped message exposed verbatim.
var ErrBranchWriteDenied = errors.New("branch write denied")

// Pusher identifies who initiated a receive-pack run so observers can
// attribute the resulting commit_pushed events. Exactly one of UserID /
// AgentRole is set:
//
//   - UserID > 0: a human pushed via cookie / PAT / password.
//   - AgentRole != "": an agent session pushed via session token; the role
//     is the snapshot RoleKey from agent_sessions (the same role key used
//     when the agent posts a comment or review_vote, so the timeline
//     renders one consistent "@agent-<role>" identity across event kinds).
//
// Both zero means the push wasn't tied to an authenticated identity (only
// possible on code paths that bypass authorizeGitWrite — currently none).
type Pusher struct {
	UserID    int64
	AgentRole string
}

// PushRefUpdate is one ref update command parsed from the receive-pack pkt-line
// stream. OldSHA and NewSHA are 40-char hex; NewSHA is zero-valued ("0000...0")
// for a branch deletion.
type PushRefUpdate struct {
	RefName string
	OldSHA  string
	NewSHA  string
}

// PostReceiveContrib carries information about a contribution that was
// recognised during PostReceive so the HTTP handler can inject a `remote:`
// sideband message into the git push response, giving the pusher instant
// feedback about the contribution ID and next steps.
type PostReceiveContrib struct {
	ContributionID int64
	RefName        string
	AgentRole      string
	HeadSHA        string
}

// PushObserver is notified before and after each receive-pack run so other
// modules can sync sidecars (the M4 issue-mode hook) and write
// commit_pushed events. PreReceive runs after authorization but before the
// receive-pack subprocess so observers can update on-disk sidecars; failures
// abort the push. PostReceive runs after the subprocess returns; failures
// are logged but don't change the push outcome — the client already got its
// response.
//
// Both PreReceive and PostReceive receive the parsed ref-update commands so
// observers can act on the exact refs that moved. PostReceive also receives a
// Pusher so observers writing audit events can attribute them correctly. The
// pack data has already been extracted into the repo before PreReceive runs,
// so the new SHA is resolvable; by PostReceive the refs themselves are updated.
//
// PostReceive returns a slice of PostReceiveContrib — one per contribution
// branch that was successfully upserted. The HTTP handler encodes these as
// sideband progress messages and injects them into the git push response
// stream so the pusher sees contribution_id and next-step hints without
// needing a follow-up API call.
type PushObserver interface {
	PreReceive(ctx context.Context, repo *Repo, fsPath string, refUpdates []PushRefUpdate) error
	PostReceive(ctx context.Context, repo *Repo, fsPath string, pusher Pusher, refUpdates []PushRefUpdate) ([]PostReceiveContrib, error)
}

// ErrBranchDiverged is returned by PreReceive observers when a push is
// rejected because the branch has diverged from its base (non-fast-forward).
// Handlers map this to HTTP 409 Conflict rather than 500.
var ErrBranchDiverged = errors.New("branch has diverged from its base branch")
