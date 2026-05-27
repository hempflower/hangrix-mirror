package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
			SessionID:    r.ID,
			RunnerID:     r.RunnerID,
			RepoID:       repoID,
			Issue:        issue,
			RoleKey:      r.RoleKey,
			Status:       string(r.Status),
			RepoSHA:      r.RepoSHA,
			CauseKind:    r.CauseKind,
			CauseID:      r.CauseID,
			RoleConfig:   cfg,
			ExitCode:               r.ExitCode,
			ErrorMessage:           r.ErrorMessage,
			CreatedAt:              r.CreatedAt,
			EndedAt:                r.EndedAt,
			ContainerID:            r.ContainerID,
			ContainerLastUsedAt:    r.ContainerLastUsedAt,
			ContainerStoppedAt:     r.ContainerStoppedAt,
			ContainerStopPending:   r.ContainerStopPending,
			ContainerCleanupPending: r.ContainerCleanupPending,
			RunningJobs:            r.RunningJobs,
		})
	}
	return out, nil
}

// ListRecent satisfies domain.Auditor. Returns one page of the most-recent
// sessions across the platform alongside the unbounded total matching the
// same filter set. Powers the admin global audit view.
func (a *Auditor) ListRecent(ctx context.Context, opts domain.RecentFilter) ([]domain.AuditSession, int64, error) {
	filter := runnerdomain.SessionFilter{
		RoleKey: opts.RoleKey,
		Status:  opts.Status,
		RepoID:  opts.RepoID,
		Since:   opts.Since,
	}
	rows, err := a.runner.ListRecentSessions(ctx, filter, runnerdomain.SessionPage{
		Offset: opts.Offset,
		Limit:  opts.Limit,
	})
	if err != nil {
		return nil, 0, err
	}
	total, err := a.runner.CountRecentSessions(ctx, filter)
	if err != nil {
		return nil, 0, err
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
			SessionID:    r.ID,
			RunnerID:     r.RunnerID,
			RepoID:       repoID,
			Issue:        issue,
			RoleKey:      r.RoleKey,
			Status:       string(r.Status),
			RepoSHA:      r.RepoSHA,
			CauseKind:    r.CauseKind,
			CauseID:      r.CauseID,
			RoleConfig:   cfg,
			ExitCode:               r.ExitCode,
			ErrorMessage:           r.ErrorMessage,
			CreatedAt:              r.CreatedAt,
			EndedAt:                r.EndedAt,
			ContainerID:            r.ContainerID,
			ContainerLastUsedAt:    r.ContainerLastUsedAt,
			ContainerStoppedAt:     r.ContainerStoppedAt,
			ContainerStopPending:   r.ContainerStopPending,
			ContainerCleanupPending: r.ContainerCleanupPending,
			RunningJobs:            r.RunningJobs,
		})
	}
	return out, total, nil
}

// GetSession returns one session converted to the AuditSession DTO.
// Maps the runner module's ErrNotFound to ErrSessionNotFound so the
// caller can branch on the sentinel without importing runner/domain.
func (a *Auditor) GetSession(ctx context.Context, sessionID int64) (*domain.AuditSession, error) {
	r, err := a.runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, runnerdomain.ErrSessionNotFound) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, fmt.Errorf("get session: %w", err)
	}
	if r == nil {
		return nil, domain.ErrSessionNotFound
	}
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
	return &domain.AuditSession{
		SessionID:              r.ID,
		RunnerID:               r.RunnerID,
		RepoID:                 repoID,
		Issue:                  issue,
		RoleKey:                r.RoleKey,
		Status:                 string(r.Status),
		RepoSHA:                r.RepoSHA,
		CauseKind:              r.CauseKind,
		CauseID:                r.CauseID,
		RoleConfig:             cfg,
		ExitCode:               r.ExitCode,
		ErrorMessage:           r.ErrorMessage,
		CreatedAt:              r.CreatedAt,
		EndedAt:                r.EndedAt,
		ContainerID:            r.ContainerID,
		ContainerLastUsedAt:    r.ContainerLastUsedAt,
		ContainerStoppedAt:     r.ContainerStoppedAt,
		ContainerStopPending:   r.ContainerStopPending,
		ContainerCleanupPending: r.ContainerCleanupPending,
		RunningJobs:            r.RunningJobs,
	}, nil
}

// ListMessages returns every message frame for a session in seq order.
// Empty slice for a session that hasn't yet produced any output. The
// caller is responsible for verifying the session belongs to whatever
// repo / issue scope they enforce — this method does not range-check.
func (a *Auditor) ListMessages(ctx context.Context, sessionID int64) ([]domain.SessionMessage, error) {
	rows, err := a.runner.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	out := make([]domain.SessionMessage, 0, len(rows))
	for _, m := range rows {
		payload := json.RawMessage(m.Payload)
		if len(payload) == 0 {
			payload = json.RawMessage("null")
		}
		out = append(out, domain.SessionMessage{
			ID:         m.ID,
			Seq:        m.Seq,
			Kind:       string(m.Kind),
			Role:       m.Role,
			Content:    m.Content,
			EventName:  m.EventName,
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
			Payload:    payload,
			CreatedAt:  m.CreatedAt,
		})
	}
	return out, nil
}

var _ domain.Auditor = (*Auditor)(nil)
