package service

import (
	"context"
	"errors"
	"fmt"

	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	issuegatedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue_gate/domain"
)

// Gate implements issuegatedomain.IssueActivityGate by consulting the
// issue module's Store. It is a thin adapter — the issue module owns
// persistence and state, and this module only adds the terminal-check
// policy.
type Gate struct {
	issues issuedomain.Store
}

type GateDeps struct {
	Issues issuedomain.Store
}

func NewGate(deps *GateDeps) *Gate {
	return &Gate{issues: deps.Issues}
}

func (g *Gate) CheckIssue(ctx context.Context, repoID int64, issueNumber int32) error {
	iss, err := g.issues.GetByNumber(ctx, repoID, int64(issueNumber))
	if err != nil {
		if errors.Is(err, issuedomain.ErrIssueNotFound) {
			return nil // not our concern — issue may be mid-creation
		}
		return fmt.Errorf("issue_gate: lookup issue #%d: %w", issueNumber, err)
	}

	var reason issuegatedomain.Reason
	switch iss.State {
	case issuedomain.StateClosed:
		reason = issuegatedomain.ReasonClosed
	case issuedomain.StateMerged:
		reason = issuegatedomain.ReasonMerged
	default:
		return nil // open — allowed
	}

	return &issuegatedomain.ErrIssueTerminal{
		RepoID:      repoID,
		IssueNumber: issueNumber,
		State:       reason,
	}
}
