-- +goose Up
-- Replace llm_providers.created_by (FK → users) with actor_id (FK → actors).

-- 1. Add the column (nullable initially).
ALTER TABLE llm_providers ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- 2. Backfill from actors via the old created_by (user PK).
UPDATE llm_providers p
SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user' AND a.user_id = p.created_by
  AND p.actor_id IS NULL;

-- 3. Fallback: system actor for any unmapped rows.
UPDATE llm_providers SET actor_id = 1 WHERE actor_id IS NULL;

-- 4. Add FK constraint.
ALTER TABLE llm_providers
    ADD CONSTRAINT fk_llm_providers_actor
    FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- 5. Make NOT NULL.
ALTER TABLE llm_providers ALTER COLUMN actor_id SET NOT NULL;

-- 6. Drop old column.
ALTER TABLE llm_providers DROP COLUMN created_by;

-- +goose Down
ALTER TABLE llm_providers ADD COLUMN IF NOT EXISTS created_by BIGINT;
UPDATE llm_providers SET created_by = 1 WHERE created_by IS NULL;
ALTER TABLE llm_providers ALTER COLUMN created_by SET NOT NULL;
ALTER TABLE llm_providers
    ADD CONSTRAINT fk_llm_providers_created_by
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT;
ALTER TABLE llm_providers DROP CONSTRAINT IF EXISTS fk_llm_providers_actor;
ALTER TABLE llm_providers DROP COLUMN IF EXISTS actor_id;
