-- ---- actors ----

-- name: GetActorByID :one
SELECT id, kind, display_name,
       user_id, agent_session_id, workflow_run_id,
       repo_id, role_key, bot_handle,
       created_at
FROM actors
WHERE id = sqlc.arg('id');

-- name: GetActorByRef :one
-- Look up an actor by its natural key. Only the columns relevant to the
-- given kind are used in the WHERE clause; NULL-safe comparisons prevent
-- false matches across kinds.
SELECT id, kind, display_name,
       user_id, agent_session_id, workflow_run_id,
       repo_id, role_key, bot_handle,
       created_at
FROM actors
WHERE kind = sqlc.arg('kind')
  AND (sqlc.arg('kind')::TEXT != 'user'           OR user_id = sqlc.narg('user_id'))
  AND (sqlc.arg('kind')::TEXT != 'agent_session'  OR agent_session_id = sqlc.narg('agent_session_id'))
  AND (sqlc.arg('kind')::TEXT != 'agent_role'     OR (repo_id = sqlc.narg('repo_id') AND role_key = sqlc.arg('role_key')))
  AND (sqlc.arg('kind')::TEXT != 'workflow_run'   OR workflow_run_id = sqlc.narg('workflow_run_id'))
  AND (sqlc.arg('kind')::TEXT != 'bot'            OR bot_handle = sqlc.arg('bot_handle'))
  AND (sqlc.arg('kind')::TEXT != 'system'         OR id = 1);

-- name: EnsureUser :one
-- Idempotent upsert for kind='user'. Reads display_name from the users
-- table on insert so it's always current.
INSERT INTO actors (kind, display_name, user_id)
SELECT 'user', u.username, sqlc.arg('user_id')
FROM users u WHERE u.id = sqlc.arg('user_id')
ON CONFLICT (user_id) WHERE kind = 'user' DO NOTHING
RETURNING id, kind, display_name, user_id, agent_session_id, workflow_run_id, repo_id, role_key, bot_handle, created_at;

-- name: EnsureAgentRole :one
-- Idempotent upsert for kind='agent_role'. Display name is derived from
-- the role key in @agent-<key> format.
INSERT INTO actors (kind, display_name, repo_id, role_key)
VALUES ('agent_role', '@agent-' || sqlc.arg('role_key'), sqlc.arg('repo_id'), sqlc.arg('role_key'))
ON CONFLICT (repo_id, role_key) WHERE kind = 'agent_role' DO NOTHING
RETURNING id, kind, display_name, user_id, agent_session_id, workflow_run_id, repo_id, role_key, bot_handle, created_at;

-- name: EnsureAgentSession :one
-- Idempotent upsert for kind='agent_session'.
INSERT INTO actors (kind, display_name, agent_session_id)
SELECT 'agent_session',
       COALESCE(NULLIF(s.role, ''), '?') || ' #' || s.id,
       sqlc.arg('agent_session_id')
FROM agent_sessions s WHERE s.id = sqlc.arg('agent_session_id')
ON CONFLICT (agent_session_id) WHERE kind = 'agent_session' DO NOTHING
RETURNING id, kind, display_name, user_id, agent_session_id, workflow_run_id, repo_id, role_key, bot_handle, created_at;

-- name: EnsureWorkflowRun :one
-- Idempotent upsert for kind='workflow_run'.
INSERT INTO actors (kind, display_name, workflow_run_id)
SELECT 'workflow_run',
       'workflow/' || wr.workflow_name || '#' || wr.id,
       sqlc.arg('workflow_run_id')
FROM workflow_runs wr WHERE wr.id = sqlc.arg('workflow_run_id')
ON CONFLICT (workflow_run_id) WHERE kind = 'workflow_run' DO NOTHING
RETURNING id, kind, display_name, user_id, agent_session_id, workflow_run_id, repo_id, role_key, bot_handle, created_at;

-- name: EnsureBot :one
-- Idempotent upsert for kind='bot'. Reserved for PAT/OAuth apps.
INSERT INTO actors (kind, display_name, bot_handle)
VALUES ('bot', sqlc.arg('bot_handle'), sqlc.arg('bot_handle'))
ON CONFLICT (bot_handle) WHERE kind = 'bot' DO NOTHING
RETURNING id, kind, display_name, user_id, agent_session_id, workflow_run_id, repo_id, role_key, bot_handle, created_at;

-- name: SystemActor :one
-- Returns the singleton system actor (id=1), seeded by the migration.
SELECT id, kind, display_name,
       user_id, agent_session_id, workflow_run_id,
       repo_id, role_key, bot_handle,
       created_at
FROM actors
WHERE id = 1;
