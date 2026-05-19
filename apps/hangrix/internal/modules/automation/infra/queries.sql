-- name: CreateAutomationRun :one
INSERT INTO automation_runs (repo_id, task_name, status, started_at)
VALUES (sqlc.arg('repo_id'), sqlc.arg('task_name'), 'running', NOW())
RETURNING *;

-- name: CompleteAutomationRun :execrows
UPDATE automation_runs
SET status       = 'success',
    issue_id     = sqlc.arg('issue_id'),
    finished_at  = NOW()
WHERE id = sqlc.arg('id');

-- name: FailAutomationRun :execrows
UPDATE automation_runs
SET status        = 'failed',
    error_message = sqlc.arg('error_message'),
    finished_at   = NOW()
WHERE id = sqlc.arg('id');

-- name: LastSuccessfulAutomationRun :one
SELECT *
FROM automation_runs
WHERE repo_id   = sqlc.arg('repo_id')
  AND task_name = sqlc.arg('task_name')
  AND status    = 'success'
ORDER BY created_at DESC
LIMIT 1;

-- name: RecentAutomationRunExists :one
SELECT EXISTS (
    SELECT 1
    FROM automation_runs
    WHERE repo_id    = sqlc.arg('repo_id')
      AND task_name  = sqlc.arg('task_name')
      AND created_at > sqlc.arg('since')
) AS exists;

-- name: ListAutomationRuns :many
SELECT *
FROM automation_runs
WHERE repo_id = sqlc.arg('repo_id')
  AND (sqlc.narg('task_name')::TEXT IS NULL OR task_name = sqlc.narg('task_name'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit');

-- name: ListAllRepos :many
SELECT
    r.id,
    r.name,
    r.default_branch,
    COALESCE(u.username, o.name) AS owner_name,
    CASE WHEN r.owner_user_id IS NOT NULL THEN 'user' ELSE 'org' END AS owner_kind,
    COALESCE(r.owner_user_id, r.owner_org_id) AS owner_id,
    COALESCE(r.owner_user_id,
        (SELECT om.user_id FROM organization_members om
         WHERE om.org_id = r.owner_org_id AND om.role = 'owner'
         LIMIT 1)
    ) AS author_user_id
FROM repos r
LEFT JOIN users u ON r.owner_user_id = u.id
LEFT JOIN organizations o ON r.owner_org_id = o.id
ORDER BY r.id;

