-- Actor module queries.
-- Ensure* methods use a two-step pattern: INSERT ... ON CONFLICT DO NOTHING
-- RETURNING id, then fall back to a SELECT by the kind-specific key. The
-- partial unique indexes in 00001_create_actors.sql guarantee at-most-one
-- row per kind+key, so the INSERT is safe under concurrent calls (ON
-- CONFLICT DO NOTHING silently discards the loser) and the follow-up
-- SELECT always finds the winner.

-- name: InsertActor :one
INSERT INTO actors (kind, display_name, user_id, role_key, workflow_run_id, agent_session_id, bot_id)
VALUES (
    sqlc.arg('kind'),
    sqlc.arg('display_name'),
    sqlc.narg('user_id'),
    sqlc.narg('role_key'),
    sqlc.narg('workflow_run_id'),
    sqlc.narg('agent_session_id'),
    sqlc.narg('bot_id')
)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: GetActorByID :one
SELECT * FROM actors WHERE id = sqlc.arg('id');

-- name: GetActorByUserID :one
SELECT id FROM actors WHERE kind = 'user' AND user_id = sqlc.arg('user_id');

-- name: GetActorByRoleKey :one
SELECT id FROM actors WHERE kind = 'agent' AND role_key = sqlc.arg('role_key');

-- name: GetActorByAgentSessionID :one
SELECT id FROM actors WHERE kind = 'agent_session' AND agent_session_id = sqlc.arg('agent_session_id');

-- name: GetActorByBotID :one
SELECT id FROM actors WHERE kind = 'bot' AND bot_id = sqlc.arg('bot_id');

-- name: GetActorByWorkflowRunID :one
SELECT id FROM actors WHERE kind = 'workflow' AND workflow_run_id = sqlc.arg('workflow_run_id');

-- name: GetSystemActorID :one
SELECT id FROM actors WHERE kind = 'system' LIMIT 1;
