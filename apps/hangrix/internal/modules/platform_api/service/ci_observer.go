package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
)

// issueRefPatterns matches git refs that contain an issue number.
// Matches: issue/<num>, issue-<num>/*, refs/heads/issue/<num>, refs/heads/issue-<num>/*
var issueRefPattern = regexp.MustCompile(`(?:^|/)issue[/-](\d+)\b`)

// CIStatusObserver implements workflowdomain.RunStatusObserver by bridging
// workflow run status transitions to agent-session Spawner.OnTrigger calls
// for the ci.status_changed event. Only runs whose ref can be associated
// with an issue number are propagated; runs on other branches (main,
// feature/*) are silently skipped.
type CIStatusObserver struct {
	spawner agentsessiondomain.Spawner
}

// CIStatusObserverDeps wires the observer's dependencies through ioc.
type CIStatusObserverDeps struct {
	Spawner agentsessiondomain.Spawner
}

// NewCIStatusObserver creates a CIStatusObserver.
func NewCIStatusObserver(deps *CIStatusObserverDeps) *CIStatusObserver {
	return &CIStatusObserver{spawner: deps.Spawner}
}

// OnRunStatusChanged emits a ci.status_changed trigger for issue-associated
// workflow runs. Best-effort: errors are logged and never propagated.
func (o *CIStatusObserver) OnRunStatusChanged(ctx context.Context, oldStatus workflowdomain.RunStatus, run *workflowdomain.WorkflowRun) error {
	if o.spawner == nil {
		return nil
	}
	issueNumber := parseIssueRef(run.Ref)
	if issueNumber <= 0 {
		return nil // not associated with an issue
	}

	// Map run status to a human-readable conclusion.
	conclusion := ""
	switch run.Status {
	case workflowdomain.RunStatusSuccess:
		conclusion = "success"
	case workflowdomain.RunStatusFailed:
		conclusion = "failure"
	case workflowdomain.RunStatusCancelled:
		conclusion = "cancelled"
	}

	payload, _ := json.Marshal(map[string]any{
		"run_id":        run.ID,
		"status":        string(run.Status),
		"conclusion":    conclusion,
		"workflow_name": run.WorkflowName,
		"repo_id":       run.RepoID,
		"commit_sha":    run.CommitSHA,
		"old_status":    string(oldStatus),
	})

	actorID := int64(0)
	if run.TriggerActor != nil {
		actorID = run.TriggerActor.UserID
	}

	causeID := fmt.Sprintf("wfrun-%d-%s", run.ID, run.Status)
	if _, err := o.spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:     agentsconfig.TriggerCIStatusChanged,
		CauseKind:   agentsessiondomain.CauseKindCIStatusChanged,
		CauseID:     causeID,
		RepoID:      run.RepoID,
		IssueNumber: issueNumber,
		ActorID:     actorID,
		Payload:     payload,
	}); err != nil {
		log.Printf("platform_api: CIStatusObserver run=%d issue=%d: %v", run.ID, issueNumber, err)
	}
	return nil
}

// parseIssueRef extracts the issue number from a git ref like
// "issue/214", "issue-214/server/foo", "refs/heads/issue-214/...", etc.
// Returns 0 when no issue number can be parsed.
func parseIssueRef(ref string) int32 {
	if ref == "" {
		return 0
	}
	m := issueRefPattern.FindStringSubmatch(ref)
	if len(m) >= 2 {
		n, err := strconv.ParseInt(m[1], 10, 32)
		if err == nil && n > 0 {
			return int32(n)
		}
	}
	return 0
}
