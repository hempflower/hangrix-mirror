-- +goose Up
-- Complete the agent_sessions actor migration (started in 00005):
--   1. Backfill created_by_actor_id from created_by via actors.
--   2. Add FK constraint, make NOT NULL, drop old created_by.
--   3. Add actor_id as a self-referencing FK (the session's own actor identity).
--      Nullable: at INSERT time the session's actor row may not exist yet;
--      callers backfill it after EnsureAgentSession creates the actor row.
-- IF NOT EXISTS is acceptable: the columns are either added once or already
-- present; the backfill is idempotent (null columns only).

-- Ensure every user has a corresponding actor row before backfill.
INSERT INTO actors (kind, user_id, display_name)
SELECT 'user', u.id, u.username
FROM users u
WHERE NOT EXISTS (
    SELECT 1 FROM actors a WHERE a.kind = 'user' AND a.user_id = u.id
);

-- Backfill created_by_actor_id for sessions with a non-null created_by.
-- Only touches rows where created_by_actor_id is still NULL.
UPDATE agent_sessions s
SET created_by_actor_id = a.id
FROM actors a
WHERE a.kind = 'user'
  AND a.user_id = s.created_by
  AND s.created_by_actor_id IS NULL
  AND s.created_by IS NOT NULL;

-- Any remaining sessions without a creator actor (system-created, legacy)
-- fall back to the system actor (id=1, seeded by the actors migration).
UPDATE agent_sessions
SET created_by_actor_id = 1
WHERE created_by_actor_id IS NULL;

-- Add FK constraint to actors.
ALTER TABLE agent_sessions
    ADD CONSTRAINT fk_agent_sessions_created_by_actor
    FOREIGN KEY (created_by_actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- Safe to enforce NOT NULL now that every row has a value.
ALTER TABLE agent_sessions ALTER COLUMN created_by_actor_id SET NOT NULL;

-- Drop the old column.
ALTER TABLE agent_sessions DROP COLUMN created_by;

-- Add actor_id as a self-referencing FK to actors.
-- Nullable on insert (chicken-and-egg: the session row must exist before
-- EnsureAgentSession can create the actor row); callers set it after.
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS actor_id BIGINT;

ALTER TABLE agent_sessions
    ADD CONSTRAINT fk_agent_sessions_actor
    FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE agent_sessions DROP CONSTRAINT IF EXISTS fk_agent_sessions_actor;
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS actor_id;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS created_by BIGINT;
ALTER TABLE agent_sessions DROP CONSTRAINT IF EXISTS fk_agent_sessions_created_by_actor;
ALTER TABLE agent_sessions ALTER COLUMN created_by_actor_id DROP NOT NULL;
ALTER TABLE agent_sessions
    ADD CONSTRAINT fk_agent_sessions_created_by
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT;
