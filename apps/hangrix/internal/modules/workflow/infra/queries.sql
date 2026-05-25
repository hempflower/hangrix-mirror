-- Workflow module queries.
-- Naming convention: sqlc.arg('name') for named parameters.

-- ---- workflow_runs ----

-- name: CreateWorkflowRun :one
INSERT INTO workflow_runs (
    repo_id, workflow_name, source_file, status, event_name,
    cause_id, ref, commit_sha, container_snapshot_json, trigger_payload_json,
    workflow_token
) VALUES (
    sqlc.arg('repo_id'), sqlc.arg('workflow_name'), sqlc.arg('source_file'),
    'pending', sqlc.arg('event_name'),
    sqlc.narg('cause_id'), sqlc.arg('ref'), sqlc.arg('commit_sha'),
    sqlc.narg('container_snapshot_json'), sqlc.narg('trigger_payload_json'),
    sqlc.arg('workflow_token')
) RETURNING *;

-- name: GetWorkflowRunByToken :one
SELECT repo_id, status FROM workflow_runs
WHERE workflow_token = sqlc.arg('token') AND workflow_token <> '';

-- name: GetWorkflowRun :one
SELECT * FROM workflow_runs WHERE id = sqlc.arg('id');

-- name: ListWorkflowRunsByRepo :many
SELECT *, COUNT(*) OVER() AS total_count
FROM workflow_runs
WHERE repo_id = sqlc.arg('repo_id')
  AND (sqlc.arg('workflow_name') = '' OR workflow_name = sqlc.arg('workflow_name'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: MarkWorkflowRunStarted :exec
UPDATE workflow_runs
SET status = 'running', started_at = NOW()
WHERE id = sqlc.arg('id') AND status = 'pending';

-- name: MarkWorkflowRunTerminal :exec
UPDATE workflow_runs
SET status = sqlc.arg('status'), finished_at = NOW()
WHERE id = sqlc.arg('id') AND status IN ('pending', 'running');

-- ---- workflow_job_runs ----

-- name: CreateWorkflowJobRun :one
INSERT INTO workflow_job_runs (
    workflow_run_id, job_key, display_name, status, sequence_index,
    working_directory, timeout_minutes, env_json, steps_json
) VALUES (
    sqlc.arg('workflow_run_id'), sqlc.arg('job_key'), sqlc.arg('display_name'),
    'pending', sqlc.arg('sequence_index'),
    sqlc.arg('working_directory'), sqlc.arg('timeout_minutes'),
    sqlc.narg('env_json'), sqlc.narg('steps_json')
) RETURNING *;

-- name: GetWorkflowJobRun :one
SELECT * FROM workflow_job_runs WHERE id = sqlc.arg('id');

-- name: ListWorkflowJobRunsByRun :many
SELECT * FROM workflow_job_runs
WHERE workflow_run_id = sqlc.arg('workflow_run_id')
ORDER BY sequence_index ASC;

-- name: ClaimNextWorkflowJob :one
-- Only claim a job if no earlier-sequence job in the same run is still
-- pending or running. This preserves the sequential execution guarantee:
-- jobs within a workflow run execute one at a time in sequence order.
UPDATE workflow_job_runs
SET status = 'running', runner_id = sqlc.arg('runner_id'), started_at = NOW()
WHERE id = (
    SELECT j.id FROM workflow_job_runs j
    WHERE j.status = 'pending'
      AND NOT EXISTS (
        SELECT 1 FROM workflow_job_runs j2
        WHERE j2.workflow_run_id = j.workflow_run_id
          AND j2.sequence_index < j.sequence_index
          AND j2.status NOT IN ('success', 'skipped', 'failed', 'cancelled')
      )
    ORDER BY j.sequence_index ASC, j.created_at ASC, j.id ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkWorkflowJobRunning :exec
UPDATE workflow_job_runs
SET status = 'running', runner_id = sqlc.arg('runner_id'), started_at = NOW()
WHERE id = sqlc.arg('id');

-- name: MarkWorkflowJobTerminal :exec
UPDATE workflow_job_runs
SET status = sqlc.arg('status'),
    exit_code = sqlc.narg('exit_code'),
    error_message = sqlc.arg('error_message'),
    finished_at = NOW()
WHERE id = sqlc.arg('id');

-- name: SkipRemainingWorkflowJobs :exec
UPDATE workflow_job_runs
SET status = 'skipped', finished_at = NOW()
WHERE workflow_run_id = sqlc.arg('workflow_run_id')
  AND status = 'pending'
  AND sequence_index > sqlc.arg('after_sequence_index');

-- name: SetWorkflowJobContainer :exec
UPDATE workflow_job_runs
SET container_id = sqlc.arg('container_id')
WHERE id = sqlc.arg('id');

-- ---- workflow_job_logs ----

-- name: AppendWorkflowJobLog :exec
INSERT INTO workflow_job_logs (workflow_job_run_id, stream, line)
VALUES (sqlc.arg('workflow_job_run_id'), sqlc.arg('stream'), sqlc.arg('line'));

-- name: ListWorkflowJobLogs :many
SELECT *, COUNT(*) OVER() AS total_count
FROM workflow_job_logs
WHERE workflow_job_run_id = sqlc.arg('workflow_job_run_id')
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');
