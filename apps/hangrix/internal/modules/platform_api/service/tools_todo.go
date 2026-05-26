package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// issueTodoUpdateTool is a combined create+update tool for issue todos.
// When todo_id is 0 or absent, it creates a new todo. When todo_id is set,
// it updates the existing todo's status and optionally its content.
func (r *Registry) issueTodoUpdateTool() *apidomain.Tool {
	return &apidomain.Tool{
		Name:        "issue_todo_update",
		Description: "Create or update a todo item on the current issue. Pass todo_id=0 (or omit) to create a new todo (content required); pass a valid todo_id to update an existing todo's status and optionally its content.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todo_id": map[string]any{
					"type":        "integer",
					"description": "The todo id to update. Omit or pass 0 to create a new todo.",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"todo", "in_progress", "done"},
					"description": "Todo status. Defaults to 'todo' for new todos.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Todo content. Required when creating; optional when updating.",
				},
				"position": map[string]any{
					"type":        "integer",
					"description": "Optional position for ordering (new todos only). Default 0.",
				},
			},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (apidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}

			var req struct {
				TodoID   int64  `json:"todo_id"`
				Status   string `json:"status"`
				Content  string `json:"content"`
				Position int    `json:"position"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}

			status := issuedomain.TodoStatus(strings.TrimSpace(req.Status))
			if status == "" {
				status = issuedomain.TodoStatusTodo
			}
			if !status.Valid() {
				return errorResult("status must be todo|in_progress|done"), nil
			}

			if req.TodoID <= 0 {
				// Create path
				content := strings.TrimSpace(req.Content)
				if content == "" {
					return errorResult("content is required when creating a todo"), nil
				}
				todo, err := r.deps.Todos.CreateTodo(ctx, scope.issue.ID, content, status, req.Position)
				if err != nil {
					return errorResult("create todo: " + err.Error()), nil
				}
				return textResult(todoToDTO(todo)), nil
			}

			// Update path — verify the todo belongs to the current issue
			// before allowing an update, preventing cross-issue tampering.
			existing, err := r.deps.Todos.GetTodo(ctx, req.TodoID)
			if err != nil {
				return errorResult("get todo: " + err.Error()), nil
			}
			if existing.IssueID != scope.issue.ID {
				return errorResult("todo does not belong to the current issue"), nil
			}
			var contentPtr *string
			if strings.TrimSpace(req.Content) != "" {
				c := strings.TrimSpace(req.Content)
				contentPtr = &c
			}
			todo, err := r.deps.Todos.UpdateTodoStatus(ctx, req.TodoID, status, contentPtr)
			if err != nil {
				return errorResult("update todo: " + err.Error()), nil
			}
			return textResult(todoToDTO(todo)), nil
		},
	}
}

// issueTodoListTool returns the todos and todo_summary for the current issue.
// Lighter than issue_read when the agent only wants to check todo state.
func (r *Registry) issueTodoListTool() *apidomain.Tool {
	return &apidomain.Tool{
		Name:        "issue_todo_list",
		Description: "List todos for the current issue with a completion summary. Lighter than issue_read — use when you only need todo state.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (apidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			todos, summary, err := r.loadTodos(ctx, scope.issue.ID)
			if err != nil {
				return errorResult("load todos: " + err.Error()), nil
			}
			return textResult(map[string]any{
				"todos":        todosToDTO(todos),
				"todo_summary": todoSummaryToDTO(summary),
			}), nil
		},
	}
}

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

// todoToDTO maps a domain todo to the agent-facing wire shape.
func todoToDTO(t *issuedomain.Todo) map[string]any {
	return map[string]any{
		"id":         t.ID,
		"issue_id":   t.IssueID,
		"content":    t.Content,
		"status":     string(t.Status),
		"position":   t.Position,
		"created_at": stableTime(t.CreatedAt),
		"updated_at": stableTime(t.UpdatedAt),
	}
}

// todosToDTO is a convenience slice variant.
func todosToDTO(todos []*issuedomain.Todo) []map[string]any {
	out := make([]map[string]any, 0, len(todos))
	for _, t := range todos {
		out = append(out, todoToDTO(t))
	}
	return out
}

// todoSummaryToDTO maps a TodoSummary to the agent-facing wire shape.
func todoSummaryToDTO(s *issuedomain.TodoSummary) map[string]any {
	if s == nil {
		return map[string]any{"total": 0, "todo": 0, "in_progress": 0, "done": 0, "all_done": false}
	}
	return map[string]any{
		"total":       s.Total,
		"todo":        s.Todo,
		"in_progress": s.InProgress,
		"done":        s.Done,
		"all_done":    s.CompletedAll(),
	}
}
