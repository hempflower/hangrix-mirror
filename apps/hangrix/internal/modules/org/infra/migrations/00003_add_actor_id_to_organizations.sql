-- +goose Up
-- Replace organizations.created_by (FK to users) with actor_id (FK to actors).
-- IF NOT EXISTS is acceptable here because this is a one-time column migration;
-- the column either exists from this migration or it doesn't. Backfill ensures
-- all existing rows get a valid actor_id before the NOT NULL constraint lands.

-- Ensure every user has a corresponding actor row before backfill.
INSERT INTO actors (kind, user_id, display_name)
SELECT 'user', u.id, u.username
FROM users u
WHERE NOT EXISTS (
    SELECT 1 FROM actors a WHERE a.kind = 'user' AND a.user_id = u.id
);

-- Add the new column nullable first so we can backfill.
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: map created_by (user id) -> actor_id via the actors table.
UPDATE organizations o
SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user' AND a.user_id = o.created_by;

-- Add FK constraint to actors.
ALTER TABLE organizations
    ADD CONSTRAINT fk_organizations_actor
    FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- Safe to enforce NOT NULL now that every row has a value.
ALTER TABLE organizations ALTER COLUMN actor_id SET NOT NULL;

-- Drop the old column (the FK to users goes with it).
ALTER TABLE organizations DROP COLUMN created_by;

-- +goose Down
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS created_by BIGINT;
ALTER TABLE organizations DROP CONSTRAINT IF EXISTS fk_organizations_actor;
ALTER TABLE organizations DROP COLUMN IF EXISTS actor_id;
ALTER TABLE organizations
    ADD CONSTRAINT fk_organizations_created_by
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT;
