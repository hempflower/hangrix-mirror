-- ---- plan_state ----

-- name: GetPlanState :one
SELECT epic_issue_id, status, max_concurrency,
       auto_step_budget, auto_steps_used, updated_at
FROM plan_state
WHERE epic_issue_id = sqlc.arg('epic_issue_id');

-- name: CreatePlanState :one
INSERT INTO plan_state (epic_issue_id)
VALUES (sqlc.arg('epic_issue_id'))
ON CONFLICT (epic_issue_id) DO NOTHING
RETURNING epic_issue_id, status, max_concurrency,
          auto_step_budget, auto_steps_used, updated_at;

-- name: SetPlanStateStatus :exec
UPDATE plan_state
SET status = sqlc.arg('status'), updated_at = NOW()
WHERE epic_issue_id = sqlc.arg('epic_issue_id');

-- name: IncPlanStateStepsUsed :exec
UPDATE plan_state
SET auto_steps_used = auto_steps_used + sqlc.arg('delta'),
    updated_at = NOW()
WHERE epic_issue_id = sqlc.arg('epic_issue_id');

-- name: SetPlanStateBudget :exec
UPDATE plan_state
SET auto_step_budget = sqlc.arg('budget'), updated_at = NOW()
WHERE epic_issue_id = sqlc.arg('epic_issue_id');

-- name: SetPlanStateConcurrency :exec
UPDATE plan_state
SET max_concurrency = sqlc.arg('n'), updated_at = NOW()
WHERE epic_issue_id = sqlc.arg('epic_issue_id');

-- name: ListActivePlanStates :many
SELECT epic_issue_id, status, max_concurrency,
       auto_step_budget, auto_steps_used, updated_at
FROM plan_state
WHERE status = 'active'
ORDER BY epic_issue_id;
