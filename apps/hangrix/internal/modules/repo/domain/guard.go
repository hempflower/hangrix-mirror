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

// PushObserver is notified before and after each receive-pack run so other
// modules can sync sidecars (the M4 issue-mode hook) and write
// commit_pushed events. PreReceive runs after authorization but before the
// receive-pack subprocess so observers can update on-disk sidecars; failures
// abort the push. PostReceive runs after the subprocess returns; failures
// are logged but don't change the push outcome — the client already got its
// response.
//
// PostReceive receives a Pusher so observers writing audit events can
// attribute them correctly. PreReceive doesn't take one because sidecar
// refresh is identity-agnostic.
type PushObserver interface {
	PreReceive(ctx context.Context, repo *Repo, fsPath string) error
	PostReceive(ctx context.Context, repo *Repo, fsPath string, pusher Pusher) error
}
