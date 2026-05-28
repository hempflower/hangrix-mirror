-- +goose Up
--
-- Replace attachments.author_id + agent_role (FK → users XOR agent_role)
-- with actor_id (FK → actors). The XOR CHECK constraint is dropped because
-- actor_id unambiguously identifies the uploader regardless of kind.
--
-- IF NOT EXISTS is acceptable here because this is a baseline-style one-time
-- migration within a coordinated multi-module release. The column either
-- exists from this migration or it doesn't; no other module will independently
-- add it. Idempotent on re-run is an operational safety net.
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: user path — author_id → actor via actors.user_id.
UPDATE attachments SET actor_id = a.id
FROM actors a
WHERE a.user_id = attachments.author_id
  AND a.kind = 'user'
  AND attachments.actor_id IS NULL;

-- Backfill: agent_role path — agent_role → actor via actors.agent_role_key.
-- attachments doesn't have a repo_id column, so we match on role_key alone.
UPDATE attachments SET actor_id = a.id
FROM actors a
WHERE a.agent_role_key = attachments.agent_role
  AND a.kind = 'agent_role'
  AND attachments.actor_id IS NULL;

-- System fallback for any remaining NULLs.
UPDATE attachments SET actor_id = 1 WHERE actor_id IS NULL;

ALTER TABLE attachments ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE attachments ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;
ALTER TABLE attachments DROP CONSTRAINT IF EXISTS attachments_author_xor_agent;
ALTER TABLE attachments DROP COLUMN author_id;
ALTER TABLE attachments DROP COLUMN agent_role;

-- +goose Down
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS author_id BIGINT;
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS agent_role TEXT NOT NULL DEFAULT '';
UPDATE attachments SET author_id = a.user_id
FROM actors a
WHERE a.id = attachments.actor_id AND a.kind = 'user';
UPDATE attachments SET agent_role = COALESCE(a.agent_role_key, '')
FROM actors a
WHERE a.id = attachments.actor_id AND a.kind = 'agent_role';
ALTER TABLE attachments DROP COLUMN IF EXISTS actor_id;
ALTER TABLE attachments
    ADD CONSTRAINT attachments_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    );
