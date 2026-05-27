package infra

import (
	"context"
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/infra/issuedb"
)

// Plan returns the full plan tree rooted at the given issue.
func (s *PostgresStore) Plan(ctx context.Context, repoID, rootNumber int64) (*domain.PlanTree, error) {
	// Load the root issue to get its ID.
	root, err := s.GetByNumber(ctx, repoID, rootNumber)
	if err != nil {
		return nil, err
	}

	// Fetch the full subtree (recursive CTE).
	rows, err := s.q.PlanSubtree(ctx, root.ID)
	if err != nil {
		return nil, fmt.Errorf("plan: subtree query: %w", err)
	}

	// Build a lookup: nodeID → row.
	nodeRows := make(map[int64]issuedb.PlanSubtreeRow, len(rows))
	childrenMap := make(map[int64][]int64) // parentID → childIDs
	for _, r := range rows {
		nodeRows[r.ID] = r
		if r.ParentID > 0 {
			childrenMap[r.ParentID] = append(childrenMap[r.ParentID], r.ID)
		}
	}

	// Fetch dependencies for the entire subtree in one query.
	depsMap, err := s.ListForSubtree(ctx, root.ID)
	if err != nil {
		return nil, fmt.Errorf("plan: deps query: %w", err)
	}

	// Build a state map for all subtree nodes (for ready/blocked calculation).
	stateMap := make(map[int64]domain.State, len(rows))
	numberToID := make(map[int64]int64, len(rows))
	for _, r := range rows {
		stateMap[r.ID] = domain.State(r.State)
		numberToID[r.Number] = r.ID
	}

	// Build the plan tree recursively.
	planRoot, rollup := buildPlanNode(root.ID, nodeRows, childrenMap, depsMap, stateMap, numberToID, make(map[int64]bool))

	// Collect ready frontier.
	ready := collectReady(planRoot)

	return &domain.PlanTree{
		Root:   planRoot,
		Rollup: rollup,
		Ready:  ready,
	}, nil
}

func buildPlanNode(
	nodeID int64,
	nodeRows map[int64]issuedb.PlanSubtreeRow,
	childrenMap map[int64][]int64,
	depsMap map[int64][]*domain.Dependency,
	stateMap map[int64]domain.State,
	numberToID map[int64]int64,
	hasRunningSession map[int64]bool,
) (*domain.PlanNode, domain.PlanRollup) {
	r, ok := nodeRows[nodeID]
	if !ok {
		return nil, domain.PlanRollup{}
	}

	node := &domain.PlanNode{
		Number:    r.Number,
		Title:     r.Title,
		State:     domain.State(r.State),
		AgentRole: r.AgentRole,
		DependsOn: []int64{},
		Children:  []*domain.PlanNode{},
	}

	// Actor.
	if r.ActorKind != "" {
		node.Actor = &domain.Actor{
			Kind:        r.ActorKind,
			UserID:      r.ActorUserID,
			RoleKey:     r.ActorRoleKey,
			DisplayName: r.ActorDisplayName,
		}
	}

	// Dependencies.
	deps := depsMap[nodeID]
	depStates := make(map[int64]domain.State)
	for _, d := range deps {
		node.DependsOn = append(node.DependsOn, d.DependsOnID)
		if st, ok := stateMap[d.DependsOnID]; ok {
			depStates[d.DependsOnID] = st
		}
	}

	// Compute ready/blocked.
	hasOpenChildren := false
	for _, childID := range childrenMap[nodeID] {
		if cr, ok := nodeRows[childID]; ok && cr.State != string(domain.StateMerged) && cr.State != string(domain.StateClosed) {
			hasOpenChildren = true
			break
		}
	}

	rs := domain.ComputeReadyState(
		&domain.Issue{Number: r.Number, State: domain.State(r.State)},
		deps, depStates,
		hasRunningSession[nodeID], 0, // todoInProgress=0; caller may refine later
		hasOpenChildren,
	)
	node.Blocked = rs.Blocked
	node.Ready = rs.Ready

	// Build children recursively.
	rollup := domain.PlanRollup{}
	childIDs := childrenMap[nodeID]
	for _, childID := range childIDs {
		childNode, childRollup := buildPlanNode(childID, nodeRows, childrenMap, depsMap, stateMap, numberToID, hasRunningSession)
		if childNode != nil {
			node.Children = append(node.Children, childNode)
			rollup.TotalLeaves += childRollup.TotalLeaves
			rollup.Merged += childRollup.Merged
			rollup.InReview += childRollup.InReview
			rollup.InProgress += childRollup.InProgress
			rollup.Open += childRollup.Open
			rollup.Closed += childRollup.Closed
		}
	}

	// This node's own contribution to rollup.
	// Only leaf nodes count toward the leaf totals.
	if len(childIDs) == 0 {
		rollup.TotalLeaves++
		switch domain.State(r.State) {
		case domain.StateMerged:
			rollup.Merged++
		case domain.StateClosed:
			rollup.Closed++
		case domain.StateOpen:
			// Open leaves: classify further (review / in-progress / plain open).
			// For now, treat all as "open"; caller may refine with review/todo data.
			rollup.Open++
		}
	}

	return node, rollup
}

// collectReady walks the plan tree and collects numbers of ready leaves.
func collectReady(node *domain.PlanNode) []int64 {
	if node == nil {
		return nil
	}
	var ready []int64
	if node.Ready {
		ready = append(ready, node.Number)
	}
	for _, child := range node.Children {
		ready = append(ready, collectReady(child)...)
	}
	return ready
}
