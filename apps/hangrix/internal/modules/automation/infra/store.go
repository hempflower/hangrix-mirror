package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/infra/automationdb"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store via sqlc-generated queries.
type PostgresStore struct {
	q *automationdb.Queries
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
	// Repos forces the repo module's migrations to run before our own:
	// 00001_automation_runs.sql has an FK on repos(id), so booting against
	// a fresh DB would otherwise hit "relation repos does not exist".
	Repos repodomain.Store
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	_ = deps.Repos // ordering-only dependency — see PostgresStoreDeps doc.
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("automation migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_automation", "."); err != nil {
		panic(fmt.Errorf("apply automation migrations: %w", err))
	}
	return &PostgresStore{
		q: automationdb.New(deps.Pool),
	}
}

func (s *PostgresStore) CreateRun(ctx context.Context, repoID int64, taskName string) (*domain.AutomationRun, error) {
	row, err := s.q.CreateAutomationRun(ctx, automationdb.CreateAutomationRunParams{
		RepoID:   repoID,
		TaskName: taskName,
	})
	if err != nil {
		return nil, err
	}
	return toDomain(&row), nil
}

func (s *PostgresStore) CompleteRun(ctx context.Context, id int64, issueID int64) error {
	_, err := s.q.CompleteAutomationRun(ctx, automationdb.CompleteAutomationRunParams{
		ID: id,
		IssueID: pgtype.Int8{
			Int64: issueID,
			Valid: true,
		},
	})
	return err
}

func (s *PostgresStore) FailRun(ctx context.Context, id int64, errMsg string) error {
	_, err := s.q.FailAutomationRun(ctx, automationdb.FailAutomationRunParams{
		ID: id,
		ErrorMessage: pgtype.Text{
			String: errMsg,
			Valid:  true,
		},
	})
	return err
}

func (s *PostgresStore) LastSuccessfulRun(ctx context.Context, repoID int64, taskName string) (*domain.AutomationRun, error) {
	row, err := s.q.LastSuccessfulAutomationRun(ctx, automationdb.LastSuccessfulAutomationRunParams{
		RepoID:   repoID,
		TaskName: taskName,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return toDomain(&row), nil
}

func (s *PostgresStore) RecentRunExists(ctx context.Context, repoID int64, taskName string, within time.Duration) (bool, error) {
	since := time.Now().Add(-within)
	return s.q.RecentAutomationRunExists(ctx, automationdb.RecentAutomationRunExistsParams{
		RepoID:   repoID,
		TaskName: taskName,
		Since: pgtype.Timestamptz{
			Time:  since,
			Valid: true,
		},
	})
}

func (s *PostgresStore) ListRuns(ctx context.Context, repoID int64, taskName string, limit int32) ([]*domain.AutomationRun, error) {
	params := automationdb.ListAutomationRunsParams{
		RepoID: repoID,
		Limit:  limit,
	}
	if taskName != "" {
		params.TaskName = pgtype.Text{String: taskName, Valid: true}
	}
	rows, err := s.q.ListAutomationRuns(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.AutomationRun, 0, len(rows))
	for i := range rows {
		out = append(out, toDomain(&rows[i]))
	}
	return out, nil
}

func toDomain(row *automationdb.AutomationRun) *domain.AutomationRun {
	return &domain.AutomationRun{
		ID:           row.ID,
		RepoID:       row.RepoID,
		TaskName:     row.TaskName,
		IssueID:      pgtypeInt8ToPtr(row.IssueID),
		Status:       domain.Status(row.Status),
		ErrorMessage: pgtypeTextToPtr(row.ErrorMessage),
		StartedAt:    row.StartedAt.Time,
		FinishedAt:   pgtypeTimestamptzToPtr(row.FinishedAt),
		CreatedAt:    row.CreatedAt.Time,
	}
}

func pgtypeInt8ToPtr(v pgtype.Int8) *int64 {
	if v.Valid {
		return &v.Int64
	}
	return nil
}

func pgtypeTextToPtr(v pgtype.Text) *string {
	if v.Valid {
		return &v.String
	}
	return nil
}

func pgtypeTimestamptzToPtr(v pgtype.Timestamptz) *time.Time {
	if v.Valid {
		return &v.Time
	}
	return nil
}
