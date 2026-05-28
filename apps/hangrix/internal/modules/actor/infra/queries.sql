-- name: GetActorByID :one
SELECT * FROM actors WHERE id = sqlc.arg('id');

-- name: GetActorByRef :one
-- Look up an existing actor by (kind, discriminant). The caller
-- constructs the WHERE clause by filling only the relevant column
-- for the kind; the other discriminants are NULL and will not match
-- any row of a different kind.
SELECT * FROM actors
WHERE kind = sqlc.arg('kind')
  AND (
    (sqlc.narg('user_id')::BIGINT IS NOT NULL AND user_id = sqlc.narg('user_id'))
    OR (sqlc.narg('agent_role_key')::TEXT IS NOT NULL AND agent_role_key = sqlc.narg('agent_role_key'))
    OR (sqlc.narg('agent_session_id')::BIGINT IS NOT NULL AND agent_session_id = sqlc.narg('agent_session_id'))
    OR (sqlc.narg('workflow_run_id')::BIGINT IS NOT NULL AND workflow_run_id = sqlc.narg('workflow_run_id'))
  );

-- name: EnsureUser :one
-- Idempotent: ON CONFLICT on the partial unique index for (kind='user', user_id)
-- returns the existing row when the tuple already exists.
INSERT INTO actors (kind, user_id, display_name)
VALUES ('user', sqlc.arg('user_id'), sqlc.arg('display_name'))
ON CONFLICT (user_id) WHERE kind = 'user' DO UPDATE
    SET display_name = EXCLUDED.display_name,
        updated_at = NOW()
RETURNING *;

-- name: EnsureAgentRole :one
INSERT INTO actors (kind, agent_role_key, display_name)
VALUES ('agent_role', sqlc.arg('agent_role_key'), sqlc.arg('display_name'))
ON CONFLICT (agent_role_key) WHERE kind = 'agent_role' DO UPDATE
    SET display_name = EXCLUDED.display_name,
        updated_at = NOW()
RETURNING *;

-- name: EnsureAgentSession :one
INSERT INTO actors (kind, agent_session_id, agent_role_key, display_name)
VALUES ('agent_session', sqlc.arg('agent_session_id'), sqlc.arg('role_key'), sqlc.arg('display_name'))
ON CONFLICT (agent_session_id) WHERE kind = 'agent_session' DO UPDATE
    SET display_name = EXCLUDED.display_name,
        agent_role_key = EXCLUDED.agent_role_key,
        updated_at = NOW()
RETURNING *;

-- name: EnsureWorkflowRun :one
INSERT INTO actors (kind, workflow_run_id, display_name)
VALUES ('workflow_run', sqlc.arg('workflow_run_id'), sqlc.arg('display_name'))
ON CONFLICT (workflow_run_id) WHERE kind = 'workflow_run' DO UPDATE
    SET display_name = EXCLUDED.display_name,
        updated_at = NOW()
RETURNING *;

-- name: EnsureBot :one
INSERT INTO actors (kind, agent_role_key, display_name)
VALUES ('bot', sqlc.arg('name'), sqlc.arg('display_name'))
ON CONFLICT (agent_role_key) WHERE kind = 'bot' DO UPDATE
    SET display_name = EXCLUDED.display_name,
        updated_at = NOW()
RETURNING *;
