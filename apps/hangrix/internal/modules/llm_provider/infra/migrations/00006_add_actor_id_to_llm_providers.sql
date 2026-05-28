-- +goose Up
--
-- Replace llm_providers.created_by (FK → users) with actor_id (FK → actors).
--
-- IF NOT EXISTS is acceptable here because this is a baseline-style one-time
-- migration within a coordinated multi-module release. The column either
-- exists from this migration or it doesn't; no other module will independently
-- add it. Idempotent on re-run is an operational safety net.
ALTER TABLE llm_providers ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: join actors on user_id for rows created by human users.
UPDATE llm_providers SET actor_id = a.id
FROM actors a
WHERE a.user_id = llm_providers.created_by
  AND a.kind = 'user'
  AND llm_providers.actor_id IS NULL;

-- System fallback for any remaining NULLs.
UPDATE llm_providers SET actor_id = 1 WHERE actor_id IS NULL;

ALTER TABLE llm_providers ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE llm_providers ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;
ALTER TABLE llm_providers DROP COLUMN created_by;

-- +goose Down
ALTER TABLE llm_providers ADD COLUMN IF NOT EXISTS created_by BIGINT;
UPDATE llm_providers SET created_by = a.user_id
FROM actors a
WHERE a.id = llm_providers.actor_id AND a.kind = 'user';
ALTER TABLE llm_providers DROP COLUMN IF EXISTS actor_id;
