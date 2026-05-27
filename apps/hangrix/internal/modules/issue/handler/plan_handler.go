package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// plan returns the plan tree for an issue (epic view).
func (h *Handler) plan(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}

	tree, err := h.issues.Plan(r.Context(), rc.repo.ID, iss.Number)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tree == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"root":   nil,
			"rollup": domain.PlanRollup{},
			"ready":  []int64{},
		})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tree)
}

// listDependencies returns the dependency edges for an issue.
func (h *Handler) listDependencies(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}

	if h.deps == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dependency store not available")
		return
	}

	dependsOn, blocks, err := h.deps.ListFor(r.Context(), iss.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Resolve issue numbers for the dependency IDs.
	depNumbers := lookupIssueNumbers(r.Context(), h.issues, dependsOn)
	blockNumbers := lookupIssueNumbers(r.Context(), h.issues, blocks)

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"depends_on": depNumbers,
		"blocks":     blockNumbers,
	})
}

// addDependency creates a dependency edge: this issue depends on another.
func (h *Handler) addDependency(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}

	if h.deps == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dependency store not available")
		return
	}

	var req struct {
		DependsOn int64 `json:"depends_on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DependsOn <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "depends_on is required")
		return
	}

	// Resolve target issue to its ID.
	target, err := h.issues.GetByNumber(r.Context(), rc.repo.ID, req.DependsOn)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "target issue not found")
		return
	}

	d, err := h.deps.Add(r.Context(), rc.repo.ID, iss.ID, target.ID, 0)
	if err != nil {
		if err == domain.ErrDependencyCycle {
			httpx.WriteJSON(w, http.StatusConflict, map[string]any{
				"error": "dependency would create a cycle",
				"code":  "dependency_cycle",
			})
			return
		}
		if err == domain.ErrDependencySelf {
			httpx.WriteError(w, http.StatusBadRequest, "an issue cannot depend on itself")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if d == nil {
		// Already exists (idempotent).
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "already_exists"})
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":           d.ID,
		"issue_id":     d.IssueID,
		"depends_on_id": d.DependsOnID,
	})
}

// removeDependency deletes a dependency edge.
func (h *Handler) removeDependency(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}

	if h.deps == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dependency store not available")
		return
	}

	dependsOnStr := chi.URLParam(r, "depends_on")
	dependsOn, err := strconv.ParseInt(dependsOnStr, 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid depends_on")
		return
	}

	// Resolve target issue to its ID.
	target, err := h.issues.GetByNumber(r.Context(), rc.repo.ID, dependsOn)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "target issue not found")
		return
	}

	if err := h.deps.Remove(r.Context(), iss.ID, target.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// getPlanState returns the plan_state for an epic issue.
func (h *Handler) getPlanState(w http.ResponseWriter, r *http.Request) {
	// This is a v0 placeholder. plan_state is managed by the plan_engine
	// module, which the handler does not directly import.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":           "active",
		"max_concurrency":  1,
		"auto_step_budget": 10,
		"auto_steps_used":  0,
	})
}

// lookupIssueNumbers resolves issue IDs to a list of {number, title} dicts.
func lookupIssueNumbers(ctx context.Context, store domain.Store, deps []*domain.Dependency) []map[string]any {
	if len(deps) == 0 {
		return []map[string]any{}
	}
	// We need to look up each dependency's target issue.
	// For now return simple IDs; full resolution requires repo context.
	result := make([]map[string]any, 0, len(deps))
	for _, d := range deps {
		result = append(result, map[string]any{
			"id":           d.DependsOnID,
			"dependency_id": d.ID,
		})
	}
	return result
}
