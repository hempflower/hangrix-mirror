package service

import (
	"context"
	"encoding/json"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// Auditor surfaces the agent_sessions snapshot columns as a queryable
// view. Reads only — the audit chain is append-only, the snapshot
// columns are frozen at session-spawn time and never updated after.
type Auditor struct {
	runner runnerdomain.Repo
}

type AuditorDeps struct {
	Runner runnerdomain.Repo
}

func NewAuditor(deps *AuditorDeps) *Auditor {
	return &Auditor{runner: deps.Runner}
}

// ListByIssue satisfies domain.Auditor. Returns every session ever
// spawned on the (repo, issue) in spawn order — including archived /
// failed rows so consumers can trace a commit through dead sessions.
func (a *Auditor) ListByIssue(ctx context.Context, repoID int64, issueNumber int32) ([]domain.AuditSession, error) {
	rows, err := a.runner.ListSessionsByIssue(ctx, repoID, issueNumber)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AuditSession, 0, len(rows))
	for _, r := range rows {
		var repoID int64
		if r.RepoID != nil {
			repoID = *r.RepoID
		}
		var issue int32
		if r.IssueNumber != nil {
			issue = *r.IssueNumber
		}
		cfg := json.RawMessage(r.RoleConfig)
		if len(cfg) == 0 {
			cfg = json.RawMessage("{}")
		}
		out = append(out, domain.AuditSession{
			SessionID:  r.ID,
			RunnerID:   r.RunnerID,
			RepoID:     repoID,
			Issue:      issue,
			RoleKey:    r.RoleKey,
			Status:     string(r.Status),
			AgentRepo:  r.AgentRepo,
			AgentSHA:   r.AgentSHA,
			RepoSHA:    r.RepoSHA,
			CauseKind:  r.CauseKind,
			CauseID:    r.CauseID,
			RoleConfig: cfg,
			CreatedAt:  r.CreatedAt,
			EndedAt:    r.EndedAt,
		})
	}
	return out, nil
}

var _ domain.Auditor = (*Auditor)(nil)
