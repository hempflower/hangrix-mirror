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

	depResult := make([]map[string]any, 0, len(dependsOn))
	for _, d := range dependsOn {
		iss, err := r.deps.Issues.GetByID(ctx, d.DependsOnID)
		if err != nil {
			continue
		}
		depResult = append(depResult, map[string]any{
			"number": iss.Number,
			"title":  iss.Title,
			"state":  string(iss.State),
		})
	}
	blockResult := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		iss, err := r.deps.Issues.GetByID(ctx, b.IssueID)
		if err != nil {
			continue
		}
		blockResult = append(blockResult, map[string]any{
			"number": iss.Number,
			"title":  iss.Title,
			"state":  string(iss.State),
		})
	}

	result := map[string]any{
		"depends_on": depResult,
		"blocks":     blockResult,
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}
