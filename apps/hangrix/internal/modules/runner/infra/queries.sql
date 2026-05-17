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

-- name: DeleteRunner :execrows
-- Hard-delete a runner row. agent_sessions.runner_id has ON DELETE SET
-- NULL so historical session rows survive — runner_id just goes blank
-- on them. Use this for "remove from list" semantics; for "stop running
-- but keep the row" use DisableRunner.
DELETE FROM runners WHERE id = sqlc.arg('id');

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
    agent_image, working_branch, base_branch,
    host_addendum, env, session_token_prefix, session_token_hash,
    session_token_sealed, created_by,
    repo_sha, role_key, cause_kind, cause_id, role_config
) VALUES (
    sqlc.narg('runner_id'),
    sqlc.narg('repo_id'),
    sqlc.narg('issue_number'),
    'pending',
    sqlc.arg('role'),
    sqlc.arg('model'),
    sqlc.arg('agent_image'),
    sqlc.arg('working_branch'),
    sqlc.arg('base_branch'),
    sqlc.arg('host_addendum'),
    sqlc.arg('env')::jsonb,
    sqlc.arg('session_token_prefix'),
    sqlc.arg('session_token_hash'),
    sqlc.narg('session_token_sealed'),
    sqlc.arg('created_by'),
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

-- name: ListSessionsByIssue :many
-- Returns every agent_session row for the (repo, issue) tuple in spawn
-- order. Powers the audit query view: a caller hands an issue, gets
-- back the entire role roster (with snapshot pins) that has touched it.
SELECT * FROM agent_sessions
WHERE repo_id      = sqlc.arg('repo_id')
  AND issue_number = sqlc.arg('issue_number')
ORDER BY id ASC;

-- name: ListRecentSessions :many
-- Returns the most-recent agent_sessions across the whole platform with
-- optional filters. Powers the admin "global agent sessions" audit view
-- under /api/admin/agent-sessions. Every filter is independent and
-- nullable; the caller composes whichever set of constraints applies.
SELECT * FROM agent_sessions
WHERE (sqlc.narg('role_key')::TEXT   IS NULL OR role_key   = sqlc.narg('role_key')::TEXT)
  AND (sqlc.narg('status')::TEXT     IS NULL OR status     = sqlc.narg('status')::TEXT)
  AND (sqlc.narg('repo_id')::BIGINT  IS NULL OR repo_id    = sqlc.narg('repo_id')::BIGINT)
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR created_at >= sqlc.narg('since')::TIMESTAMPTZ)
ORDER BY id DESC
LIMIT sqlc.arg('lim');

-- name: ArchiveSessionsByIssue :execrows
-- Flip every non-archived session on this (repo, issue) to archived.
-- Driven by issue.closed / issue.merged: the parent issue is the only
-- thing that can archive sessions — there is no per-session admin
-- archive button (docs/agent-config.md §"Session 模型"). Already-archived
-- rows are skipped so the call is idempotent. session_token_sealed is
-- NULL'd so a leaked DB snapshot of an archived session can't expose the
-- bearer plaintext.
UPDATE agent_sessions
SET status               = 'archived',
    ended_at             = COALESCE(ended_at, NOW()),
    session_token_sealed = NULL
WHERE repo_id      = sqlc.arg('repo_id')
  AND issue_number = sqlc.arg('issue_number')
  AND status      != 'archived';

-- name: ClaimNextSessionLock :one
-- Skip-locked claim: pins the oldest pending session this runner is
-- eligible to run. M7a relaxed the rule from "pinned-to-this-runner
-- only" to "pinned OR unpinned": the spawner now leaves runner_id NULL
-- when no pre-assignment policy applies, and any eligible runner picks
-- the row up. The follow-up ClaimSessionUpdate writes runner_id so the
-- audit row records who actually ran it.
SELECT * FROM agent_sessions
WHERE status = 'pending'
  AND (runner_id = sqlc.arg('runner_id') OR runner_id IS NULL)
ORDER BY created_at ASC, id ASC
FOR UPDATE SKIP LOCKED
LIMIT 1;

-- name: ClaimSessionUpdate :exec
-- Pins runner_id at claim time so unpinned rows record who took them.
UPDATE agent_sessions
SET status = 'claimed',
    claimed_at = NOW(),
    runner_id = sqlc.arg('runner_id')
WHERE id = sqlc.arg('id');

-- name: MarkSessionRunning :execrows
UPDATE agent_sessions
SET status = 'running', started_at = COALESCE(started_at, NOW())
WHERE id = sqlc.arg('id') AND status IN ('claimed', 'running');

-- name: MarkSessionTerminal :execrows
-- session_token_sealed is NULL'd at terminate time so a leaked DB
-- snapshot of a dead session no longer carries the bearer plaintext.
--
-- The NOT IN guard MUST list every terminal state — otherwise a late
-- runner terminate (e.g. container exited just as issue.closed fired)
-- could overwrite an `archived` row with `succeeded` and lose the
-- "session ended because the issue closed" signal in the audit chain.
-- `archived` was added in migration 00002; we updated this guard at
-- the same time the agent_session module landed.
UPDATE agent_sessions
SET status               = sqlc.arg('status'),
    ended_at             = NOW(),
    exit_code            = sqlc.narg('exit_code'),
    error_message        = sqlc.arg('error_message'),
    session_token_sealed = NULL
WHERE id = sqlc.arg('id')
  AND status NOT IN ('succeeded', 'failed', 'cancelled', 'archived');

-- name: MarkSessionIdle :execrows
-- Flip a running session to idle: one turn finished but the parent issue
-- is still live, so the row should stay reusable for the next trigger.
-- Unlike MarkSessionTerminal this DOES NOT NULL session_token_sealed —
-- the runner re-uses the same session identity when the row is rewoken,
-- so the sealed plaintext must survive across container exits. ended_at
-- is intentionally left NULL because the session as a logical unit
-- isn't done; only the most recent container is.
UPDATE agent_sessions
SET status    = 'idle',
    exit_code = sqlc.narg('exit_code')
WHERE id = sqlc.arg('id')
  AND status IN ('claimed', 'running');

-- name: ResumeSession :execrows
-- User-initiated resume: flip an idle / failed / succeeded row back to
-- 'pending' so the next runner poll picks it up. Re-mints the session
-- token (the previous sealed plaintext was NULL'd on terminate for
-- failed/succeeded). For idle rows the sealed token survives so we
-- pass through the existing value via the caller's sealed arg.
--
-- archived rows are not resumable — the parent issue archived them and
-- a new issue is required to start fresh. cancelled is treated as
-- failed for legacy data.
UPDATE agent_sessions
SET status               = 'pending',
    session_token_prefix = sqlc.arg('session_token_prefix'),
    session_token_hash   = sqlc.arg('session_token_hash'),
    session_token_sealed = sqlc.arg('session_token_sealed'),
    runner_id            = NULL,
    claimed_at           = NULL,
    started_at           = NULL,
    ended_at             = NULL,
    exit_code            = NULL,
    error_message        = ''
WHERE id = sqlc.arg('id')
  AND status IN ('idle', 'failed', 'succeeded', 'cancelled');

-- name: DeleteSession :execrows
-- Hard-delete a session row. Cascades remove the message log + inputs
-- queue (FK ON DELETE CASCADE in the migration). Use for the user-
-- visible "delete agent run" affordance — once a session is failed
-- and the user doesn't want it recoverable, this clears the row.
DELETE FROM agent_sessions WHERE id = sqlc.arg('id');

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
