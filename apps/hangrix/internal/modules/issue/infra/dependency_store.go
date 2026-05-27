package infra

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/infra/issuedb"
)

// ---- DependencyStore implementation ----

// Add creates a dependency edge. Returns ErrDependencyCycle if the new edge
// would create a cycle (i.e. dependsOnID is already reachable from issueID).
func (s *PostgresStore) Add(ctx context.Context, repoID, issueID, dependsOnID, createdBy int64) (*domain.Dependency, error) {
	if issueID == dependsOnID {
		return nil, domain.ErrDependencySelf
	}

	// Cycle check: can we reach issueID from dependsOnID forward?
	// If yes, adding issueID → dependsOnID would create a cycle.
	reachable, err := s.reachableForward(ctx, dependsOnID, issueID)
	if err != nil {
		return nil, err
	}
	if reachable {
		return nil, domain.ErrDependencyCycle
	}

	var createdByArg pgtype.Int8
	if createdBy > 0 {
		createdByArg = pgtype.Int8{Int64: createdBy, Valid: true}
	}

	row, err := s.q.AddDependency(ctx, issuedb.AddDependencyParams{
		RepoID:      repoID,
		IssueID:     issueID,
		DependsOnID: dependsOnID,
		CreatedBy:   createdByArg,
	})
	if err != nil {
		return nil, err
	}
	if row.ID == 0 {
		// ON CONFLICT DO NOTHING — already exists (idempotent).
		return nil, nil
	}
	return depFromFields(depFields{ID: row.ID, RepoID: row.RepoID, IssueID: row.IssueID, DependsOnID: row.DependsOnID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}), nil
}

// Remove deletes a dependency edge.
func (s *PostgresStore) Remove(ctx context.Context, issueID, dependsOnID int64) error {
	return s.q.RemoveDependency(ctx, issuedb.RemoveDependencyParams{
		IssueID:     issueID,
		DependsOnID: dependsOnID,
	})
}

// ListFor returns all edges incident to issueID grouped by direction.
func (s *PostgresStore) ListFor(ctx context.Context, issueID int64) (dependsOn []*domain.Dependency, blocks []*domain.Dependency, err error) {
	depRows, err := s.q.ListDependenciesFor(ctx, issueID)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range depRows {
		dependsOn = append(dependsOn, depFromFields(depFields{ID: r.ID, RepoID: r.RepoID, IssueID: r.IssueID, DependsOnID: r.DependsOnID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}))
	}

	blockRows, err := s.q.ListDependenciesBlocking(ctx, issueID)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range blockRows {
		blocks = append(blocks, depFromFields(depFields{ID: r.ID, RepoID: r.RepoID, IssueID: r.IssueID, DependsOnID: r.DependsOnID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}))
	}
	return dependsOn, blocks, nil
}

// ReachableForward checks whether there is a directed path from fromIssueID
// to toIssueID. Used for cycle detection before adding a new edge.
// Implemented as a raw pgxpool query because the recursive CTE confuses
// sqlc's parser (see queries.sql note).
func (s *PostgresStore) ReachableForward(ctx context.Context, fromIssueID, toIssueID int64) (bool, error) {
	return s.reachableForward(ctx, fromIssueID, toIssueID)
}

func (s *PostgresStore) reachableForward(ctx context.Context, fromIssueID, toIssueID int64) (bool, error) {
	const query = `
		WITH RECURSIVE fwd AS (
			SELECT depends_on_id FROM issue_dependencies WHERE issue_id = $1
		  UNION
			SELECT d.depends_on_id FROM issue_dependencies d JOIN fwd ON d.issue_id = fwd.depends_on_id
		)
		SELECT COUNT(*) FROM fwd WHERE depends_on_id = $2
	`
	var cnt int64
	err := s.pool.QueryRow(ctx, query, fromIssueID, toIssueID).Scan(&cnt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return cnt > 0, nil
}

// ListForSubtree returns a map from issue ID to its dependency edges
// (depends_on direction) for every issue in the subtree rooted at rootIssueID.
func (s *PostgresStore) ListForSubtree(ctx context.Context, rootIssueID int64) (map[int64][]*domain.Dependency, error) {
	rows, err := s.q.ListDepsForSubtree(ctx, rootIssueID)
	if err != nil {
		return nil, err
	}
	result := make(map[int64][]*domain.Dependency)
	for _, r := range rows {
		d := depFromFields(depFields{ID: r.ID, RepoID: r.RepoID, IssueID: r.IssueID, DependsOnID: r.DependsOnID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt})
		result[d.IssueID] = append(result[d.IssueID], d)
	}
	return result, nil
}

// depFields is a common field set shared by all dependency query row types.
type depFields struct {
	ID, RepoID, IssueID, DependsOnID, CreatedBy int64
	CreatedAt                                    pgtype.Timestamptz
}

func depFromFields(f depFields) *domain.Dependency {
	return &domain.Dependency{
		ID:          f.ID,
		RepoID:      f.RepoID,
		IssueID:     f.IssueID,
		DependsOnID: f.DependsOnID,
		CreatedBy:   f.CreatedBy,
		CreatedAt:   f.CreatedAt.Time,
	}
}
