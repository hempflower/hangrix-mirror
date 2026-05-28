-- +goose Up
-- Replace attachments.author_id / agent_role XOR pattern with a single
-- actor_id FK to the actors table. The XOR constraint is replaced by a
-- simpler NOT NULL on actor_id.

-- 1. Add the column (nullable initially).
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- 2. Backfill user-authored rows (author_id IS NOT NULL).
UPDATE attachments att
SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user' AND a.user_id = att.author_id
  AND att.actor_id IS NULL
  AND att.author_id IS NOT NULL;

-- 3. Backfill agent-authored rows (agent_role <> '').
UPDATE attachments att
SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent' AND a.role_key = att.agent_role
  AND att.actor_id IS NULL
  AND att.agent_role <> '';

-- 4. Fallback: system actor for any unmapped rows.
UPDATE attachments SET actor_id = 1 WHERE actor_id IS NULL;

-- 5. Drop the XOR constraint and old columns.
ALTER TABLE attachments DROP CONSTRAINT IF EXISTS attachments_author_xor_agent;
ALTER TABLE attachments DROP COLUMN author_id;
ALTER TABLE attachments DROP COLUMN agent_role;

-- 6. Add FK and make NOT NULL.
ALTER TABLE attachments
    ADD CONSTRAINT fk_attachments_actor
    FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE SET NULL;
ALTER TABLE attachments ALTER COLUMN actor_id SET NOT NULL;

-- +goose Down
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS author_id BIGINT;
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS agent_role TEXT NOT NULL DEFAULT '';
-- Reverse backfill: best-effort — set author_id from user actors,
-- leave agent-authored as author_id=NULL with agent_role.
UPDATE attachments att
SET author_id = a.user_id
FROM actors a
WHERE a.id = att.actor_id AND a.kind = 'user'
  AND att.author_id IS NULL;
-- For agent-authored: can't recover the original agent_role from
-- the actor row's role_key alone (the actor may have been created
-- with a display_name that differs from the role_key). Best-effort:
-- set agent_role from the actor's role_key.
UPDATE attachments att
SET agent_role = COALESCE(a.role_key, '')
FROM actors a
WHERE a.id = att.actor_id AND a.kind IN ('agent', 'agent_session')
  AND att.agent_role = '';
ALTER TABLE attachments
    ADD CONSTRAINT attachments_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    );
ALTER TABLE attachments DROP CONSTRAINT IF EXISTS fk_attachments_actor;
ALTER TABLE attachments DROP COLUMN IF EXISTS actor_id;
