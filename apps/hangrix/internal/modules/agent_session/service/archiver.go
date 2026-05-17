package service

import (
	"context"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// Archiver flips every non-archived session on a (repo, issue) to
// 'archived'. Thin shim over runner.Repo.ArchiveSessionsByIssue —
// archival is a SQL UPDATE; nothing in this layer needs to be smart
// about it. The interface exists so the issue handler depends on a
// domain.Archiver rather than the runner module's wider Repo surface.
type Archiver struct {
	runner runnerdomain.Repo
}

type ArchiverDeps struct {
	Runner runnerdomain.Repo
}

func NewArchiver(deps *ArchiverDeps) *Archiver {
	return &Archiver{runner: deps.Runner}
}

// OnIssueClosed satisfies domain.Archiver. Idempotent — re-running on
// an already-archived issue returns 0.
func (a *Archiver) OnIssueClosed(ctx context.Context, repoID int64, issueNumber int32) (int64, error) {
	return a.runner.ArchiveSessionsByIssue(ctx, repoID, issueNumber)
}

var _ domain.Archiver = (*Archiver)(nil)
