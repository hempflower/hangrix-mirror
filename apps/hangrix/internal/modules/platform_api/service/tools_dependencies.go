package service

import (
	"context"
	"encoding/json"
	"fmt"

	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// DepsAdd creates a dependency edge: the session's issue depends on
// dependsOnNumber. Runs cycle detection; returns structured error on cycle.
func (r *Registry) DepsAdd(ctx context.Context, scope *sessionScope, dependsOnNumber int64) (string, error) {
	if r.deps.Deps == nil {
		return "", fmt.Errorf("dependency store not available")
	}

	// Resolve target issue.
	target, err := r.deps.Issues.GetByNumber(ctx, scope.repo.ID, dependsOnNumber)
	if err != nil {
		return "", fmt.Errorf("target issue #%d not found", dependsOnNumber)
	}

	d, err := r.deps.Deps.Add(ctx, scope.repo.ID, scope.issue.ID, target.ID, 0)
	if err != nil {
		if err == issuedomain.ErrDependencyCycle {
			b, _ := json.Marshal(map[string]string{
				"error": "dependency would create a cycle",
				"code":  "dependency_cycle",
			})
			return string(b), nil
		}
		return "", err
	}
	if d == nil {
		return "dependency already exists", nil
	}
	return fmt.Sprintf("added dependency: #%d now depends on #%d", scope.issue.Number, dependsOnNumber), nil
}

// DepsRemove deletes a dependency edge.
func (r *Registry) DepsRemove(ctx context.Context, scope *sessionScope, dependsOnNumber int64) (string, error) {
	if r.deps.Deps == nil {
		return "", fmt.Errorf("dependency store not available")
	}

	target, err := r.deps.Issues.GetByNumber(ctx, scope.repo.ID, dependsOnNumber)
	if err != nil {
		return "", fmt.Errorf("target issue #%d not found", dependsOnNumber)
	}

	if err := r.deps.Deps.Remove(ctx, scope.issue.ID, target.ID); err != nil {
		return "", err
	}

	return fmt.Sprintf("removed dependency on #%d", dependsOnNumber), nil
}

// DepsRead returns the dependency info for the session's issue.
func (r *Registry) DepsRead(ctx context.Context, scope *sessionScope) (string, error) {
	if r.deps.Deps == nil {
		return "", fmt.Errorf("dependency store not available")
	}

	dependsOn, blocks, err := r.deps.Deps.ListFor(ctx, scope.issue.ID)
	if err != nil {
		return "", err
	}

	depNumbers := make([]int64, 0, len(dependsOn))
	for _, d := range dependsOn {
		depNumbers = append(depNumbers, d.DependsOnID)
	}
	blockNumbers := make([]int64, 0, len(blocks))
	for _, b := range blocks {
		blockNumbers = append(blockNumbers, b.IssueID)
	}

	result := map[string]any{
		"depends_on": depNumbers,
		"blocks":     blockNumbers,
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}
