package infra

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/infra/automationdb"
)

// RepoLister implements domain.RepoLister via the sqlc-generated
// ListAllRepos query.
type RepoLister struct {
	q *automationdb.Queries
}

type RepoListerDeps struct {
	Pool *pgxpool.Pool
}

// NewRepoLister returns a ready-to-use RepoLister backed by sqlc.
func NewRepoLister(deps *RepoListerDeps) *RepoLister {
	return &RepoLister{q: automationdb.New(deps.Pool)}
}

// ListAll returns every repo with enough metadata for the scheduler to
// read its automation config and create issues as the appropriate user.
func (l *RepoLister) ListAll(ctx context.Context) ([]domain.RepoRef, error) {
	rows, err := l.q.ListAllRepos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RepoRef, 0, len(rows))
	for _, row := range rows {
		authorUserID := row.AuthorUserID.Int64
		if !row.AuthorUserID.Valid || authorUserID == 0 {
			continue
		}
		out = append(out, domain.RepoRef{
			ID:            row.ID,
			Name:          row.Name,
			DefaultBranch: row.DefaultBranch,
			OwnerName:     row.OwnerName,
			OwnerKind:     row.OwnerKind,
			OwnerID:       row.OwnerID.Int64,
			AuthorUserID:  authorUserID,
		})
	}
	return out, nil
}
