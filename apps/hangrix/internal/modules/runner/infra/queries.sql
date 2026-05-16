-- ---- runners ----

-- name: CreateRunner :one
INSERT INTO runners (
    name, owner_user_id, visibility, status,
    enroll_token_prefix, enroll_token_hash, created_by
) VALUES (
    sqlc.arg('name'),
    sqlc.narg('owner_user_id'),
    sqlc.arg('visibility'),
    'pending',
    sqlc.arg('enroll_token_prefix'),
    sqlc.arg('enroll_token_hash'),
    sqlc.arg('created_by')
)
RETURNING *;

-- name: GetRunnerByID :one
SELECT * FROM runners WHERE id = sqlc.arg('id');

-- name: GetRunnerByEnrollPrefixForUpdate :one
-- Used in the enrollment-redemption transaction. FOR UPDATE pins the row
-- so a concurrent redemption serialises behind us and loses cleanly.
SELECT * FROM runners
WHERE enroll_token_prefix = sqlc.arg('enroll_token_prefix')
FOR UPDATE;

-- name: GetRunnerByAgentPrefix :one
SELECT * FROM runners WHERE agent_token_prefix = sqlc.arg('agent_token_prefix');

-- name: ListRunners :many
SELECT * FROM runners
WHERE (sqlc.narg('owner_user_id')::BIGINT IS NULL OR owner_user_id = sqlc.narg('owner_user_id'))
  AND (sqlc.narg('visibility')::TEXT  IS NULL OR visibility    = sqlc.narg('visibility'))
ORDER BY id DESC;

-- name: DisableRunner :execrows
UPDATE runners
SET status = 'disabled',
    agent_token_revoked_at = COALESCE(agent_token_revoked_at, NOW()),
    updated_at = NOW()
WHERE id = sqlc.arg('id');

-- name: UpdateRunnerHeartbeat :execrows
UPDATE runners
SET last_heartbeat_at = NOW(),
    capabilities = sqlc.arg('capabilities')::jsonb,
    updated_at = NOW()
WHERE id = sqlc.arg('id');

-- name: RedeemEnrollmentUpdate :exec
-- Second half of the redemption transaction: flip the row to active,
-- mark used_at, persist the fresh agent token hash + prefix.
UPDATE runners
SET status                 = 'active',
    enroll_token_used_at   = NOW(),
    agent_token_prefix     = sqlc.arg('agent_token_prefix'),
    agent_token_hash       = sqlc.arg('agent_token_hash'),
    agent_token_revoked_at = NULL,
    capabilities           = sqlc.arg('capabilities')::jsonb,
    last_heartbeat_at      = NOW(),
    updated_at             = NOW()
WHERE id = sqlc.arg('id');

-- ---- agent_sessions ----

-- name: CreateSession :one
INSERT INTO agent_sessions (
    runner_id, repo_id, issue_number, status, role, model,
    agent_image, agent_repo, working_branch, base_branch,
    host_addendum, env, session_token_prefix, session_token_hash,
    session_token_sealed, created_by,
    agent_sha, repo_sha, role_key, cause_kind, cause_id, role_config
) VALUES (
    sqlc.narg('runner_id'),
    sqlc.narg('repo_id'),
    sqlc.narg('issue_number'),
    'pending',
    sqlc.arg('role'),
    sqlc.arg('model'),
    sqlc.arg('agent_image'),
    sqlc.arg('agent_repo'),
    sqlc.arg('working_branch'),
    sqlc.arg('base_branch'),
    sqlc.arg('host_addendum'),
    sqlc.arg('env')::jsonb,
    sqlc.arg('session_token_prefix'),
    sqlc.arg('session_token_hash'),
    sqlc.narg('session_token_sealed'),
    sqlc.arg('created_by'),
    sqlc.arg('agent_sha'),
    sqlc.arg('repo_sha'),
    sqlc.arg('role_key'),
    sqlc.arg('cause_kind'),
    sqlc.arg('cause_id'),
    sqlc.arg('role_config')::jsonb
)
RETURNING *;

-- name: GetSessionByID :one
SELECT * FROM agent_sessions WHERE id = sqlc.arg('id');

-- name: GetSessionByTokenPrefix :one
SELECT * FROM agent_sessions WHERE session_token_prefix = sqlc.arg('session_token_prefix');

-- name: ListSessions :many
SELECT * FROM agent_sessions
WHERE (sqlc.narg('runner_id')::BIGINT IS NULL OR runner_id = sqlc.narg('runner_id'))
  AND (sqlc.narg('status')::TEXT   IS NULL OR status    = sqlc.narg('status'))
ORDER BY id DESC
LIMIT sqlc.arg('lim');

-- name: ClaimNextSessionLock :one
-- Skip-locked claim: pins the oldest pending session for this runner so a
-- concurrent claim by another runner-thread races on a different row.
SELECT * FROM agent_sessions
WHERE status = 'pending' AND runner_id = sqlc.arg('runner_id')
ORDER BY created_at ASC, id ASC
FOR UPDATE SKIP LOCKED
LIMIT 1;

-- name: ClaimSessionUpdate :exec
UPDATE agent_sessions
SET status = 'claimed', claimed_at = NOW()
WHERE id = sqlc.arg('id');

-- name: MarkSessionRunning :execrows
UPDATE agent_sessions
SET status = 'running', started_at = COALESCE(started_at, NOW())
WHERE id = sqlc.arg('id') AND status IN ('claimed', 'running');

-- name: MarkSessionTerminal :execrows
-- session_token_sealed is NULL'd at terminate time so a leaked DB
-- snapshot of a dead session no longer carries the bearer plaintext.
UPDATE agent_sessions
SET status               = sqlc.arg('status'),
    ended_at             = NOW(),
    exit_code            = sqlc.narg('exit_code'),
    error_message        = sqlc.arg('error_message'),
    session_token_sealed = NULL
WHERE id = sqlc.arg('id') AND status NOT IN ('succeeded', 'failed', 'cancelled');

-- ---- messages ----

-- name: AppendMessage :one
-- COALESCE(MAX(seq)+1, 1) is racy under concurrent appends; the UNIQUE
-- (session_id, seq) constraint catches collisions and the caller retries.
INSERT INTO agent_session_messages (
    session_id, seq, kind, role, content, event_name,
    tool_call_id, tool_name, payload
)
SELECT
    sqlc.arg('session_id'),
    COALESCE(MAX(seq), 0) + 1,
    sqlc.arg('kind'),
    sqlc.arg('role'),
    sqlc.arg('content'),
    sqlc.arg('event_name'),
    sqlc.arg('tool_call_id'),
    sqlc.arg('tool_name'),
    sqlc.arg('payload')::jsonb
FROM agent_session_messages WHERE session_id = sqlc.arg('session_id')
RETURNING *;

-- name: ListMessages :many
SELECT * FROM agent_session_messages
WHERE session_id = sqlc.arg('session_id')
ORDER BY seq ASC;

-- ---- inputs queue ----

-- name: EnqueueInput :one
INSERT INTO agent_session_inputs (session_id, seq, payload)
SELECT
    sqlc.arg('session_id'),
    COALESCE(MAX(seq), 0) + 1,
    sqlc.arg('payload')::jsonb
FROM agent_session_inputs WHERE session_id = sqlc.arg('session_id')
RETURNING *;

-- name: ClaimPendingInputsLock :many
SELECT * FROM agent_session_inputs
WHERE session_id = sqlc.arg('session_id') AND consumed_at IS NULL
ORDER BY seq ASC
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg('lim');

-- name: MarkInputsConsumed :exec
UPDATE agent_session_inputs SET consumed_at = NOW() WHERE id = ANY(sqlc.arg('ids')::BIGINT[]);
