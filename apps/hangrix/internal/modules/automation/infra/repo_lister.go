package infra

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
)

// RepoLister implements domain.RepoLister by querying the repos table
// directly, joining users and organizations to determine the author
// user ID for issue creation.
type RepoLister struct {
	pool *pgxpool.Pool
}

// NewRepoLister returns a ready-to-use RepoLister.
func NewRepoLister(pool *pgxpool.Pool) *RepoLister {
	return &RepoLister{pool: pool}
}

type repoRow struct {
	ID            int64
	Name          string
	DefaultBranch string
	OwnerName     string
	OwnerKind     string
	OwnerID       int64
	AuthorUserID  int64
}

// ListAll returns every repo with enough metadata for the scheduler to
// read its automation config and create issues as the appropriate user.
func (l *RepoLister) ListAll(ctx context.Context) ([]domain.RepoRef, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT
			r.id,
			r.name,
			r.default_branch,
			COALESCE(u.username, o.name) AS owner_name,
			CASE WHEN r.owner_user_id IS NOT NULL THEN 'user' ELSE 'org' END AS owner_kind,
			COALESCE(r.owner_user_id, r.owner_org_id) AS owner_id,
			COALESCE(r.owner_user_id,
				(SELECT om.user_id FROM organization_members om
				 WHERE om.org_id = r.owner_org_id AND om.role = 'owner'
				 LIMIT 1)
			) AS author_user_id
		FROM repos r
		LEFT JOIN users u ON r.owner_user_id = u.id
		LEFT JOIN organizations o ON r.owner_org_id = o.id
		ORDER BY r.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.RepoRef
	for rows.Next() {
		var row repoRow
		if err := rows.Scan(&row.ID, &row.Name, &row.DefaultBranch,
			&row.OwnerName, &row.OwnerKind, &row.OwnerID, &row.AuthorUserID); err != nil {
			return nil, err
		}
		// Skip repos where we couldn't determine an author (org owner not found).
		if row.AuthorUserID == 0 {
			continue
		}
		out = append(out, domain.RepoRef{
			ID:            row.ID,
			Name:          row.Name,
			DefaultBranch: row.DefaultBranch,
			OwnerName:     row.OwnerName,
			OwnerKind:     row.OwnerKind,
			OwnerID:       row.OwnerID,
			AuthorUserID:  row.AuthorUserID,
		})
	}
	return out, rows.Err()
}
