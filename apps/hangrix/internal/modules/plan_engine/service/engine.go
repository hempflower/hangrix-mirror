// Package service implements the deterministic plan progression engine.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	agentsconfig "github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/plan_engine/domain"
)

// Engine implements domain.Engine.
type Engine struct {
	issues    issuedomain.Store
	deps      issuedomain.DependencyStore
	planState domain.PlanStateStore
	spawner   agentsessiondomain.Spawner

	mu       sync.Mutex
	epicLocks map[int64]*sync.Mutex
	// dispatched tracks in-flight ready dispatches per epic to avoid
	// dispatching the same ready issue twice within one tick cycle.
	dispatched map[int64]map[int64]bool // epicID → issueNumber
}

// EngineDeps captures the cross-module dependencies injected by ioc.
type EngineDeps struct {
	Issues    issuedomain.Store
	Deps      issuedomain.DependencyStore
	PlanState domain.PlanStateStore
	Spawner   agentsessiondomain.Spawner
}

// NewEngine creates the plan engine.
func NewEngine(deps *EngineDeps) *Engine {
	return &Engine{
		issues:     deps.Issues,
		deps:       deps.Deps,
		planState:  deps.PlanState,
		spawner:    deps.Spawner,
		epicLocks:  make(map[int64]*sync.Mutex),
		dispatched: make(map[int64]map[int64]bool),
	}
}

func (e *Engine) epicMu(epicID int64) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if mu, ok := e.epicLocks[epicID]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	e.epicLocks[epicID] = mu
	return mu
}

// OnChildClosed is called when a child issue is merged/closed.
func (e *Engine) OnChildClosed(ctx context.Context, repoID, parentNumber, childNumber int64) error {
	// Load the parent epic.
	parent, err := e.issues.GetByNumber(ctx, repoID, parentNumber)
	if err != nil {
		return fmt.Errorf("plan_engine: parent #%d: %w", parentNumber, err)
	}

	mu := e.epicMu(parent.ID)
	mu.Lock()
	defer mu.Unlock()

	_ = childNumber // reserved for audit

	return e.processEpic(ctx, repoID, parent)
}

// Tick scans all active epics and catches up on missed dispatches.
func (e *Engine) Tick(ctx context.Context) error {
	states, err := e.planState.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, st := range states {
		mu := e.epicMu(st.EpicIssueID)
		mu.Lock()
		if err := e.processEpicByID(ctx, st.EpicIssueID); err != nil {
			log.Printf("plan_engine: tick epic=%d: %v", st.EpicIssueID, err)
		}
		mu.Unlock()
	}
	return nil
}

// processEpic computes the ready frontier for a known epic and dispatches
// newly-ready issues.
func (e *Engine) processEpic(ctx context.Context, repoID int64, epic *issuedomain.Issue) error {
	tree, err := e.issues.Plan(ctx, repoID, epic.Number)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}

	// Load or create plan_state.
	ps, err := e.planState.GetOrCreate(ctx, epic.ID)
	if err != nil {
		return fmt.Errorf("plan_state: %w", err)
	}

	for _, readyNumber := range tree.Ready {
		if err := e.dispatchReady(ctx, repoID, epic, ps, readyNumber); err != nil {
			log.Printf("plan_engine: dispatch epic=%d issue=%d: %v", epic.Number, readyNumber, err)
		}
	}
	return nil
}

// processEpicByID is like processEpic but looks up the epic by ID.
// Used by Tick which only has epic_issue_id from plan_state.
func (e *Engine) processEpicByID(ctx context.Context, epicID int64) error {
	epic, err := e.issues.GetByID(ctx, epicID)
	if err != nil {
		return fmt.Errorf("plan_engine: lookup epic %d: %w", epicID, err)
	}
	return e.processEpic(ctx, epic.RepoID, epic)
}

// dispatchReady checks safety gates and fires issue.ready for a single
// ready leaf issue.
func (e *Engine) dispatchReady(ctx context.Context, repoID int64, epic *issuedomain.Issue,
	ps *domain.PlanState, readyNumber int64) error {

	// Deduplicate: skip if already dispatched in this cycle.
	if e.dispatched[epic.ID] != nil && e.dispatched[epic.ID][readyNumber] {
		return nil
	}

	// Safety gate: paused.
	if ps.Status == domain.PlanStatusPaused {
		return nil
	}

	// Safety gate: budget.
	if ps.AutoStepsUsed >= ps.AutoStepBudget {
		return nil
	}

	// Fire issue.ready via spawner.
	payload, _ := json.Marshal(map[string]any{
		"epic_number": epic.Number,
	})
	if _, err := e.spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueReady,
		CauseKind:   "plan_engine.dispatch",
		CauseID:     fmt.Sprintf("%d", readyNumber),
		RepoID:      repoID,
		IssueNumber: int32(readyNumber),
		Payload:     payload,
	}); err != nil {
		return err
	}

	// Track dispatch and bump budget counter.
	if e.dispatched[epic.ID] == nil {
		e.dispatched[epic.ID] = make(map[int64]bool)
	}
	e.dispatched[epic.ID][readyNumber] = true
	_ = e.planState.IncStepsUsed(ctx, epic.ID, 1)

	log.Printf("plan_engine: dispatched issue #%d for epic #%d", readyNumber, epic.Number)
	return nil
}

// StartTicker launches a background goroutine that calls Tick periodically.
// Returns a stop function that cancels the ticker.
func (e *Engine) StartTicker(interval time.Duration, parentCtx context.Context) func() {
	ctx, cancel := context.WithCancel(parentCtx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := e.Tick(ctx); err != nil {
					log.Printf("plan_engine: ticker: %v", err)
				}
			}
		}
	}()
	return cancel
}
