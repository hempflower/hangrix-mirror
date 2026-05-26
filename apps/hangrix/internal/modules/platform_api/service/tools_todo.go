package service

import (
	"context"
	"fmt"

	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// loadTodos fetches both the todo list and the summary for the given issue.
func (r *Registry) loadTodos(ctx context.Context, issueID int64) ([]*issuedomain.Todo, *issuedomain.TodoSummary, error) {
	if r.deps.Todos == nil {
		return nil, nil, fmt.Errorf("todo store not available")
	}
	todos, err := r.deps.Todos.ListTodos(ctx, issueID)
	if err != nil {
		return nil, nil, err
	}
	summary, err := r.deps.Todos.CountTodosByStatus(ctx, issueID)
	if err != nil {
		return nil, nil, err
	}
	return todos, summary, nil
}

// todosCompletionBlock returns a non-empty string when there are incomplete
// todos blocking merge/close. Returns empty when all todos are done or there
// are no todos at all (no todos = no blocker).
func (r *Registry) todosCompletionBlock(ctx context.Context, scope *sessionScope) (string, []map[string]any) {
	if r.deps.Todos == nil {
		return "", nil
	}
	todos, summary, err := r.loadTodos(ctx, scope.issue.ID)
	if err != nil {
		return "", nil // best-effort: a transient DB error shouldn't block
	}
	if summary.CompletedAll() {
		return "", nil
	}
	// Collect incomplete todos for the block detail.
	incomplete := make([]map[string]any, 0)
	for _, t := range todos {
		if t.Status != issuedomain.TodoStatusDone {
			incomplete = append(incomplete, map[string]any{
				"id":      t.ID,
				"content": t.Content,
				"status":  string(t.Status),
			})
		}
	}
	if len(incomplete) == 0 {
		return "", nil
	}
	return fmt.Sprintf(
		"%d/%d todos incomplete — all todos must be completed before merging or closing",
		summary.Total-summary.Done, summary.Total,
	), incomplete
}
