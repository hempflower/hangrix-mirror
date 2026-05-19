package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"text/template"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// Executor runs a single automation task: records the run, creates an
// issue, and updates the run status.
type Executor struct {
	runs  domain.Store
	issue issuedomain.Store
}

// ExecutorDeps wires the Executor's dependencies through ioc.
type ExecutorDeps struct {
	Runs  domain.Store
	Issue issuedomain.Store
}

// NewExecutor returns a ready-to-use Executor.
func NewExecutor(deps *ExecutorDeps) *Executor {
	return &Executor{
		runs:  deps.Runs,
		issue: deps.Issue,
	}
}

// Execute triggers a single task execution for a repo. It records the
// run, creates the issue, and updates the run status. Returns the run
// and the created issue ID on success.
func (e *Executor) Execute(ctx context.Context, repoID int64, defaultBranch string, authorUserID int64, task *agentsconfig.Task) (*domain.AutomationRun, error) {
	// 1. Insert a running row.
	run, err := e.runs.CreateRun(ctx, repoID, task.Name)
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	// 2. Build the issue body: render template + append @agent-<role> mentions.
	body := renderBody(task)
	body = appendMentions(body, task.Roles)

	// 3. Create the issue.
	issue, err := e.issue.Create(ctx, repoID, authorUserID, task.Issue.Title, body, defaultBranch, 0, 0)
	if err != nil {
		// Mark the run as failed.
		if ferr := e.runs.FailRun(ctx, run.ID, err.Error()); ferr != nil {
			log.Printf("automation executor: fail run %d: %v", run.ID, ferr)
		}
		return nil, fmt.Errorf("create issue: %w", err)
	}

	// 4. Mark the run as complete.
	if err := e.runs.CompleteRun(ctx, run.ID, issue.ID); err != nil {
		log.Printf("automation executor: complete run %d: %v", run.ID, err)
		// The issue was created; the run update is best-effort. Return
		// the issue ID so the caller knows it succeeded.
	}
	run.IssueID = &issue.ID
	run.Status = domain.StatusSuccess
	return run, nil
}

// renderBody applies Go template expansion to the issue body. v1 only
// supports {{.TaskName}} and {{.Schedule}}; {{.LastRun}} renders as "N/A".
func renderBody(task *agentsconfig.Task) string {
	tmpl, err := template.New("body").Parse(task.Issue.Body)
	if err != nil {
		// If the body has invalid template syntax, return it verbatim
		// so the issue is still created.
		return task.Issue.Body
	}
	var buf strings.Builder
	data := map[string]string{
		"TaskName": task.Name,
		"Schedule": task.Schedule,
		"LastRun":  "N/A",
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return task.Issue.Body
	}
	return buf.String()
}

// appendMentions appends @agent-<role> lines for every role in the list.
func appendMentions(body string, roles []string) string {
	if len(roles) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n\n")
	for _, role := range roles {
		fmt.Fprintf(&b, "@agent-%s\n", role)
	}
	return b.String()
}
