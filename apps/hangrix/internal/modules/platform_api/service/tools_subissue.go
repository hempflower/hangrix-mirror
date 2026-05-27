package service

import (
	"context"

	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// subIssueBlock returns a non-empty reason when any open descendant blocks
// merging or closing the session's issue. Returns empty when clear.
// Best-effort: a transient DB error returns "" (same convention as
// todosCompletionBlock).
func (r *Registry) subIssueBlock(ctx context.Context, scope *sessionScope) (string, []*issuedomain.OpenDescendant) {
	open, err := r.deps.Issues.ListOpenDescendants(ctx, scope.issue.ID)
	if err != nil {
		return "", nil
	}
	return issuedomain.SubIssueBlock(open)
}
