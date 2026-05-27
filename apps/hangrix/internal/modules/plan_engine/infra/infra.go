// Package infra holds the Postgres-backed implementation of the plan_engine
// domain. SQL lives in queries.sql; sqlc generates the typed accessors
// under planenginedb/.
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/plan_engine/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/plan_engine/infra/planenginedb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PlanStateStore implements domain.PlanStateStore.
type PlanStateStore struct {
	q    *planenginedb.Queries
	pool *pgxpool.Pool
}

type PlanStateStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPlanStateStore(deps *PlanStateStoreDeps) *PlanStateStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("plan_engine migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_plan_engine", "."); err != nil {
		panic(fmt.Errorf("apply plan_engine migrations: %w", err))
	}
	return &PlanStateStore{
		q:    planenginedb.New(deps.Pool),
		pool: deps.Pool,
	}
}

func (s *PlanStateStore) GetOrCreate(ctx context.Context, epicIssueID int64) (*domain.PlanState, error) {
	row, err := s.q.GetPlanState(ctx, epicIssueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Create default.
			row, err = s.q.CreatePlanState(ctx, epicIssueID)
			if err != nil {
				return nil, err
			}
			return planStateFromRow(row), nil
		}
		return nil, err
	}
	return planStateFromRow(row), nil
}

func (s *PlanStateStore) SetStatus(ctx context.Context, epicIssueID int64, status domain.PlanStatus) error {
	return s.q.SetPlanStateStatus(ctx, planenginedb.SetPlanStateStatusParams{
		EpicIssueID: epicIssueID,
		Status:      string(status),
	})
}

func (s *PlanStateStore) IncStepsUsed(ctx context.Context, epicIssueID int64, delta int) error {
	return s.q.IncPlanStateStepsUsed(ctx, planenginedb.IncPlanStateStepsUsedParams{
		EpicIssueID: epicIssueID,
		Delta:       int32(delta),
	})
}

func (s *PlanStateStore) SetBudget(ctx context.Context, epicIssueID int64, budget int) error {
	return s.q.SetPlanStateBudget(ctx, planenginedb.SetPlanStateBudgetParams{
		EpicIssueID: epicIssueID,
		Budget:      int32(budget),
	})
}

func (s *PlanStateStore) SetConcurrency(ctx context.Context, epicIssueID int64, n int) error {
	return s.q.SetPlanStateConcurrency(ctx, planenginedb.SetPlanStateConcurrencyParams{
		EpicIssueID: epicIssueID,
		N:           int32(n),
	})
}

// ListActive returns all plan_state rows with status='active'.
func (s *PlanStateStore) ListActive(ctx context.Context) ([]*domain.PlanState, error) {
	rows, err := s.q.ListActivePlanStates(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*domain.PlanState, 0, len(rows))
	for _, r := range rows {
		result = append(result, planStateFromRow(r))
	}
	return result, nil
}

func planStateFromRow(r planenginedb.PlanState) *domain.PlanState {
	return &domain.PlanState{
		EpicIssueID:    r.EpicIssueID,
		Status:         domain.PlanStatus(r.Status),
		MaxConcurrency: int(r.MaxConcurrency),
		AutoStepBudget: int(r.AutoStepBudget),
		AutoStepsUsed:  int(r.AutoStepsUsed),
		UpdatedAt:      r.UpdatedAt.Time,
	}
}
