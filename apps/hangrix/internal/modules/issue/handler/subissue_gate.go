package handler

import (
	"context"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// subIssueBlock returns a non-empty reason when any open descendant blocks
// merging or closing the given issue. Returns empty when clear.
// Best-effort: a transient DB error returns "".
func (h *Handler) subIssueBlock(ctx context.Context, issueID int64) (string, []*domain.OpenDescendant) {
	open, err := h.issues.ListOpenDescendants(ctx, issueID)
	if err != nil {
		return "", nil
	}
	return domain.SubIssueBlock(open)
}
