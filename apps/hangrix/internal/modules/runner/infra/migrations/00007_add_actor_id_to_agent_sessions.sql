-- +goose Up
--
-- agent_sessions: replace created_by (FK → users) with created_by_actor_id
-- (FK → actors), and add actor_id as a self-referencing FK to the session's
-- own actor row (kind='agent_session').
--
-- created_by_actor_id already exists from 00005 but has no FK yet. This
-- migration backfills it, adds the FK, adds actor_id, and drops the old
-- created_by column.
--
-- actor_id is nullable during insert (the session's actor row is created
-- after the session row), but becomes non-null once the EnsureAgentSession
-- upsert runs. This is documented as a two-step write.
--
-- IF NOT EXISTS is acceptable here because this is a baseline-style one-time
-- migration within a coordinated multi-module release. No other module will
-- independently add these columns. Idempotent on re-run is a safety net.

-- 1. Add self-referencing actor_id column (the session's own actor row).
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- 2. Backfill created_by_actor_id from the old created_by column via actors.
UPDATE agent_sessions SET created_by_actor_id = a.id
FROM actors a
WHERE a.user_id = agent_sessions.created_by
  AND a.kind = 'user'
  AND agent_sessions.created_by_actor_id IS NULL;

-- 3. System fallback for any remaining NULLs in created_by_actor_id.
UPDATE agent_sessions SET created_by_actor_id = 1 WHERE created_by_actor_id IS NULL;

-- 4. Backfill actor_id for sessions that already have an actor row (kind='agent_session').
UPDATE agent_sessions SET actor_id = a.id
FROM actors a
WHERE a.agent_session_id = agent_sessions.id
  AND a.kind = 'agent_session'
  AND agent_sessions.actor_id IS NULL;

-- 5. Add FK constraints.
ALTER TABLE agent_sessions ALTER COLUMN created_by_actor_id SET NOT NULL;
ALTER TABLE agent_sessions ADD FOREIGN KEY (created_by_actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- actor_id FK: ON DELETE RESTRICT — deleting a session's actor is an audit event.
ALTER TABLE agent_sessions ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- 6. Drop old column.
ALTER TABLE agent_sessions DROP COLUMN created_by;

-- +goose Down
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS created_by BIGINT;
UPDATE agent_sessions SET created_by = a.user_id
FROM actors a
WHERE a.id = agent_sessions.created_by_actor_id AND a.kind = 'user';
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS created_by_actor_id;
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS actor_id;
