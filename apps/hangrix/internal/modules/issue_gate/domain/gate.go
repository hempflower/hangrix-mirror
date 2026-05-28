// Package domain declares the issue activity gate: a cross-cutting check
// that blocks all agent actions on closed or merged issues. The gate is
// consumed by every agent-facing surface (platform API, LLM proxy, git
// push, session spawner) to prevent stale-agent actions after an issue
// reaches a terminal state.
//
// The gate follows the agent identity → issue gate → work layering rule
// documented in .hangrix/knowledge/architecture.md: the agent's identity
// (session token) is validated first, then the gate checks the issue
// state, and only then is the actual work dispatched.
package domain

import (
	"context"
	"fmt"
)

// Reason names the terminal issue state that blocks agent activity.
type Reason string

const (
	ReasonClosed Reason = "closed"
	ReasonMerged Reason = "merged"
)

// ErrIssueTerminal is returned by IssueActivityGate.CheckIssue when the
// target issue is closed or merged. It carries the structured context
// so consumers can render a consistent 403 envelope.
type ErrIssueTerminal struct {
	RepoID      int64
	IssueNumber int32
	State       Reason
}

func (e *ErrIssueTerminal) Error() string {
	return fmt.Sprintf(
		"Issue #%d is %s. All agent actions are prohibited on closed or merged issues. No further actions should be taken on this issue.",
		e.IssueNumber, e.State,
	)
}

// IssueActivityGate is the cross-module seam for the issue terminal-state
// check. Every agent-facing surface consumes this interface; the
// issue_gate module's service implements it.
type IssueActivityGate interface {
	// CheckIssue looks up the issue by (repoID, issueNumber) and returns
	// ErrIssueTerminal when the state is closed or merged. A nil return
	// means the issue is open and agent activity is allowed.
	CheckIssue(ctx context.Context, repoID int64, issueNumber int32) error
}
