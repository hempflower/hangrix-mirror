-- +goose Up
-- Replace runners.created_by (FK → users) with actor_id (FK → actors).
-- Batch 2 of the actor migration: after agent_sessions (00005/00006),
-- we now convert the runners table itself.

-- 1. Add the column (nullable initially).
ALTER TABLE runners ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- 2. Backfill from actors via the old created_by (user PK). The actors
--    table was populated by the 00006 backfill which ensured every user
--    has an actor row.
UPDATE runners r
SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user' AND a.user_id = r.created_by
  AND r.actor_id IS NULL;

-- 3. Fallback: any runner whose creator user has no actor row gets the
--    system actor (id=1, created by the actor module's seed migration).
UPDATE runners SET actor_id = 1 WHERE actor_id IS NULL;

-- 4. Add FK constraint.
ALTER TABLE runners
    ADD CONSTRAINT fk_runners_actor
    FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- 5. Make NOT NULL.
ALTER TABLE runners ALTER COLUMN actor_id SET NOT NULL;

-- 6. Drop old column.
ALTER TABLE runners DROP COLUMN created_by;

-- +goose Down
ALTER TABLE runners ADD COLUMN IF NOT EXISTS created_by BIGINT;
-- Reverse backfill: best-effort — use system user (id=1) since we
-- can't reliably reconstruct the original user_id from actor_id.
UPDATE runners SET created_by = 1 WHERE created_by IS NULL;
ALTER TABLE runners ALTER COLUMN created_by SET NOT NULL;
ALTER TABLE runners
    ADD CONSTRAINT fk_runners_created_by
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT;
ALTER TABLE runners DROP CONSTRAINT IF EXISTS fk_runners_actor;
ALTER TABLE runners DROP COLUMN IF EXISTS actor_id;
