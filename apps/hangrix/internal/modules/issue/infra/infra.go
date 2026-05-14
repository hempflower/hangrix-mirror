// Package infra holds the Postgres-backed implementation of the issue domain.
// Queries are written inline with pgx — sqlc is not used here because the
// query set is small and one-off (no shared queries.sql with another module),
// so the codegen step would cost more than it saves.
//
// Cross-table consistency notes:
//   - Issue.Create UPSERTs into issue_counters to mint the next per-repo
//     number atomically (single transaction).
//   - List queries always join users so the handler doesn't need a second
//     hop to render author labels.
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store on a pgx pool.
type PostgresStore struct {
	pool *pgxpool.Pool
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("issue migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_issue", "."); err != nil {
		panic(fmt.Errorf("apply issue migrations: %w", err))
	}
	return &PostgresStore{pool: deps.Pool}
}

// Create runs the counter UPSERT and the issue insert in one transaction so
// two concurrent creators can never mint the same number. When parentID is
// non-zero the child's parent_id / parent_number columns are populated and
// the caller is expected to have already pointed baseBranch at the parent's
// issue branch.
func (s *PostgresStore) Create(ctx context.Context, repoID, authorID int64, title, body, baseBranch string, parentID, parentNumber int64) (*domain.Issue, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var nextNumber int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO issue_counters (repo_id, next) VALUES ($1, 2)
		ON CONFLICT (repo_id) DO UPDATE SET next = issue_counters.next + 1
		RETURNING next - 1
	`, repoID).Scan(&nextNumber); err != nil {
		return nil, fmt.Errorf("issue: bump counter: %w", err)
	}

	branchName := fmt.Sprintf("issue/%d", nextNumber)
	var (
		id        int64
		state     string
		createdAt pgtype.Timestamptz
		updatedAt pgtype.Timestamptz
	)
	var parentArg any
	if parentID > 0 {
		parentArg = parentID
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO issues (repo_id, number, author_id, title, body, branch_name, base_branch, parent_id, parent_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, state, created_at, updated_at
	`, repoID, nextNumber, authorID, title, body, branchName, baseBranch, parentArg, parentNumber).Scan(&id, &state, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("issue: insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return s.GetByNumber(ctx, repoID, nextNumber)
}

// GetByNumber returns the issue plus author username so the handler can
// serialize a complete record without a follow-up users lookup.
func (s *PostgresStore) GetByNumber(ctx context.Context, repoID, number int64) (*domain.Issue, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT i.id, i.repo_id, i.number, i.author_id, u.username,
		       i.title, i.body, i.state, i.branch_name, i.base_branch,
		       i.head_sha, i.merge_commit_sha, i.merged_at,
		       COALESCE(i.parent_id, 0), i.parent_number,
		       i.created_at, i.updated_at
		FROM issues i
		JOIN users u ON u.id = i.author_id
		WHERE i.repo_id = $1 AND i.number = $2
	`, repoID, number)
	return scanIssue(row)
}

func (s *PostgresStore) List(ctx context.Context, repoID int64, f domain.ListFilter) ([]*domain.Issue, int64, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	args := []any{repoID}
	stateClause := ""
	if f.State != "" {
		args = append(args, string(f.State))
		stateClause = "AND i.state = $2"
	}
	args = append(args, limit, offset)

	listSQL := fmt.Sprintf(`
		SELECT i.id, i.repo_id, i.number, i.author_id, u.username,
		       i.title, i.body, i.state, i.branch_name, i.base_branch,
		       i.head_sha, i.merge_commit_sha, i.merged_at,
		       COALESCE(i.parent_id, 0), i.parent_number,
		       i.created_at, i.updated_at
		FROM issues i
		JOIN users u ON u.id = i.author_id
		WHERE i.repo_id = $1 %s
		ORDER BY i.number DESC
		LIMIT $%d OFFSET $%d
	`, stateClause, len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*domain.Issue{}
	for rows.Next() {
		iss, err := scanIssueRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, iss)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	countArgs := []any{repoID}
	countSQL := "SELECT COUNT(*) FROM issues i WHERE i.repo_id = $1"
	if f.State != "" {
		countArgs = append(countArgs, string(f.State))
		countSQL += " AND i.state = $2"
	}
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *PostgresStore) ListChildren(ctx context.Context, parentID int64) ([]*domain.Issue, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.id, i.repo_id, i.number, i.author_id, u.username,
		       i.title, i.body, i.state, i.branch_name, i.base_branch,
		       i.head_sha, i.merge_commit_sha, i.merged_at,
		       COALESCE(i.parent_id, 0), i.parent_number,
		       i.created_at, i.updated_at
		FROM issues i
		JOIN users u ON u.id = i.author_id
		WHERE i.parent_id = $1
		ORDER BY i.number ASC
	`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.Issue{}
	for rows.Next() {
		iss, err := scanIssueRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, iss)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateTitleBody(ctx context.Context, id int64, title, body string) (*domain.Issue, error) {
	var repoID, number int64
	if err := s.pool.QueryRow(ctx, `
		UPDATE issues
		SET title = $2, body = $3, updated_at = NOW()
		WHERE id = $1
		RETURNING repo_id, number
	`, id, title, body).Scan(&repoID, &number); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrIssueNotFound
		}
		return nil, err
	}
	return s.GetByNumber(ctx, repoID, number)
}

func (s *PostgresStore) UpdateState(ctx context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error) {
	var repoID, number int64
	if err := s.pool.QueryRow(ctx, `
		UPDATE issues
		SET state = $2,
		    merge_commit_sha = CASE WHEN $2 = 'merged' THEN $3 ELSE merge_commit_sha END,
		    merged_at = CASE WHEN $2 = 'merged' THEN NOW() ELSE merged_at END,
		    updated_at = NOW()
		WHERE id = $1
		RETURNING repo_id, number
	`, id, string(state), mergeSHA).Scan(&repoID, &number); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrIssueNotFound
		}
		return nil, err
	}
	return s.GetByNumber(ctx, repoID, number)
}

func (s *PostgresStore) UpdateHeadSHA(ctx context.Context, id int64, headSHA string) error {
	_, err := s.pool.Exec(ctx, `UPDATE issues SET head_sha = $2, updated_at = NOW() WHERE id = $1`, id, headSHA)
	return err
}

func (s *PostgresStore) ListOpenIssueNumbers(ctx context.Context, repoID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT number FROM issues WHERE repo_id = $1 AND state = 'open' ORDER BY number`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateComment(ctx context.Context, issueID, authorID int64, body, filePath string, line int) (*domain.Comment, error) {
	var (
		id        int64
		createdAt pgtype.Timestamptz
		updatedAt pgtype.Timestamptz
	)
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO issue_comments (issue_id, author_id, body, file_path, line)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at
	`, issueID, authorID, body, filePath, line).Scan(&id, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	// Re-read with author join so the caller has a complete object.
	row := s.pool.QueryRow(ctx, `
		SELECT c.id, c.issue_id, c.author_id, u.username, c.body, c.file_path, c.line, c.created_at, c.updated_at
		FROM issue_comments c JOIN users u ON u.id = c.author_id
		WHERE c.id = $1
	`, id)
	return scanComment(row)
}

func (s *PostgresStore) ListComments(ctx context.Context, issueID int64) ([]*domain.Comment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.issue_id, c.author_id, u.username, c.body, c.file_path, c.line, c.created_at, c.updated_at
		FROM issue_comments c JOIN users u ON u.id = c.author_id
		WHERE c.issue_id = $1
		ORDER BY c.created_at, c.id
	`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.Comment{}
	for rows.Next() {
		c, err := scanCommentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateEvent(ctx context.Context, issueID int64, kind domain.EventKind, payload []byte, actorID int64) (*domain.Event, error) {
	var actorArg any
	if actorID > 0 {
		actorArg = actorID
	}
	var (
		id        int64
		createdAt pgtype.Timestamptz
	)
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO issue_events (issue_id, kind, payload, actor_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`, issueID, string(kind), payload, actorArg).Scan(&id, &createdAt); err != nil {
		return nil, err
	}
	// Re-read with author join (left join because actor_id is nullable).
	row := s.pool.QueryRow(ctx, `
		SELECT e.id, e.issue_id, e.kind, e.payload, COALESCE(e.actor_id, 0), COALESCE(u.username, ''), e.created_at
		FROM issue_events e LEFT JOIN users u ON u.id = e.actor_id
		WHERE e.id = $1
	`, id)
	return scanEvent(row)
}

func (s *PostgresStore) ListEvents(ctx context.Context, issueID int64) ([]*domain.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.issue_id, e.kind, e.payload, COALESCE(e.actor_id, 0), COALESCE(u.username, ''), e.created_at
		FROM issue_events e LEFT JOIN users u ON u.id = e.actor_id
		WHERE e.issue_id = $1
		ORDER BY e.created_at, e.id
	`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.Event{}
	for rows.Next() {
		e, err := scanEventRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- scan helpers ---

// Both pgx.Row and pgx.Rows expose Scan; we accept either via this minimal
// interface so the row/rows callsites can share a single scan helper.
type scanner interface {
	Scan(dest ...any) error
}

func scanIssue(r pgx.Row) (*domain.Issue, error) {
	iss, err := scanIssueRow(r)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrIssueNotFound
		}
		return nil, err
	}
	return iss, nil
}

func scanIssueRow(r scanner) (*domain.Issue, error) {
	var (
		iss       domain.Issue
		mergedAt  pgtype.Timestamptz
		createdAt pgtype.Timestamptz
		updatedAt pgtype.Timestamptz
		state     string
	)
	if err := r.Scan(
		&iss.ID, &iss.RepoID, &iss.Number, &iss.AuthorID, &iss.AuthorName,
		&iss.Title, &iss.Body, &state, &iss.BranchName, &iss.BaseBranch,
		&iss.HeadSHA, &iss.MergeCommitSHA, &mergedAt,
		&iss.ParentID, &iss.ParentNumber,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	iss.State = domain.State(state)
	iss.CreatedAt = createdAt.Time
	iss.UpdatedAt = updatedAt.Time
	if mergedAt.Valid {
		t := mergedAt.Time
		iss.MergedAt = &t
	}
	return &iss, nil
}

func scanComment(r pgx.Row) (*domain.Comment, error) {
	return scanCommentRow(r)
}

func scanCommentRow(r scanner) (*domain.Comment, error) {
	var (
		c         domain.Comment
		createdAt pgtype.Timestamptz
		updatedAt pgtype.Timestamptz
	)
	if err := r.Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.AuthorName, &c.Body, &c.FilePath, &c.Line, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	c.CreatedAt = createdAt.Time
	c.UpdatedAt = updatedAt.Time
	return &c, nil
}

func scanEvent(r pgx.Row) (*domain.Event, error) {
	return scanEventRow(r)
}

func scanEventRow(r scanner) (*domain.Event, error) {
	var (
		e         domain.Event
		kind      string
		createdAt pgtype.Timestamptz
	)
	if err := r.Scan(&e.ID, &e.IssueID, &kind, &e.Payload, &e.ActorID, &e.ActorName, &createdAt); err != nil {
		return nil, err
	}
	e.Kind = domain.EventKind(kind)
	e.CreatedAt = createdAt.Time
	return &e, nil
}
