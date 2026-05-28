-- +goose Up
-- Add actor_id to access_tokens while keeping user_id (semantic = human owner).
-- The user_id column stays because tokens are fundamentally owned by a human
-- user; actor_id is the resolved actor reference for unified audit trails.

-- 1. Add the column (nullable initially).
ALTER TABLE access_tokens ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- 2. Backfill from actors via user_id.
UPDATE access_tokens t
SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user' AND a.user_id = t.user_id
  AND t.actor_id IS NULL;

-- 3. Fallback: system actor for any unmapped rows.
UPDATE access_tokens SET actor_id = 1 WHERE actor_id IS NULL;

-- 4. Add FK constraint. ON DELETE RESTRICT because tokens are owned by
--    a user; the user row should not be deletable while tokens exist.
ALTER TABLE access_tokens
    ADD CONSTRAINT fk_access_tokens_actor
    FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- 5. Make NOT NULL.
ALTER TABLE access_tokens ALTER COLUMN actor_id SET NOT NULL;

-- +goose Down
ALTER TABLE access_tokens DROP CONSTRAINT IF EXISTS fk_access_tokens_actor;
ALTER TABLE access_tokens DROP COLUMN IF EXISTS actor_id;
