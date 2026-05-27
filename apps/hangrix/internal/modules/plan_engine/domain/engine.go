// Package domain declares the plan_engine types: plan execution state,
// safety gates, and the deterministic engine interface that drives
// epic issue forward progress.
package domain

import (
	"context"
	"time"
)

// PlanStatus is the run-state of an epic plan.
type PlanStatus string

const (
	PlanStatusActive PlanStatus = "active"
	PlanStatusPaused PlanStatus = "paused"
)

func (s PlanStatus) Valid() bool {
	return s == PlanStatusActive || s == PlanStatusPaused
}

// PlanState is the per-epic run-state row from plan_state.
type PlanState struct {
	EpicIssueID    int64
	Status         PlanStatus
	MaxConcurrency int
	AutoStepBudget int
	AutoStepsUsed  int
	UpdatedAt      time.Time
}

// PlanStateStore is the persistence abstraction for plan_state rows.
type PlanStateStore interface {
	GetOrCreate(ctx context.Context, epicIssueID int64) (*PlanState, error)
	SetStatus(ctx context.Context, epicIssueID int64, s PlanStatus) error
	IncStepsUsed(ctx context.Context, epicIssueID int64, delta int) error
	SetBudget(ctx context.Context, epicIssueID int64, budget int) error
	SetConcurrency(ctx context.Context, epicIssueID int64, n int) error
	// ListActive returns all plan_state rows with status='active'.
	ListActive(ctx context.Context) ([]*PlanState, error)
}

// GateDecision is the structured result of the engine's safety-gate
// evaluation before dispatching a ready issue to a worker.
type GateDecision struct {
	Allow       bool
	Reason      string // paused | concurrency | budget_exhausted
	ActiveCount int
	BudgetUsed  int
}

// Engine is the deterministic plan-progression driver — not an agent.
// It consumes issue domain's ReadyState, checks safety gates, and
// dispatches ready leaf issues to workers via the spawner.
type Engine interface {
	// OnChildClosed is called when a child issue is merged/closed.
	// repoID and parentNumber identify the parent epic; childNumber is
	// the just-closed child (for audit). It recomputes the epic's ready
	// frontier, checks safety gates, and dispatches newly-ready issues
	// to workers via the spawner.
	OnChildClosed(ctx context.Context, repoID, parentNumber, childNumber int64) error

	// Tick is the periodic catch-up scan for all active epics.
	// Used as a cron-style fallback for lost events.
	Tick(ctx context.Context) error
}
