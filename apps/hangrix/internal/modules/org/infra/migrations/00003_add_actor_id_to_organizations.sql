-- +goose Up
--
-- Replace organizations.created_by and organization_members.added_by
-- (both FK → users) with actor_id (FK → actors).
--
-- IF NOT EXISTS is acceptable here because this is a baseline-style one-time
-- migration within a coordinated multi-module release. The columns either
-- exist from this migration or they don't; no other module will independently
-- add them. Idempotent on re-run is an operational safety net.

-- === organizations.created_by → actor_id ===
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS actor_id BIGINT;

UPDATE organizations SET actor_id = a.id
FROM actors a
WHERE a.user_id = organizations.created_by
  AND a.kind = 'user'
  AND organizations.actor_id IS NULL;

UPDATE organizations SET actor_id = 1 WHERE actor_id IS NULL;

ALTER TABLE organizations ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE organizations ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;
ALTER TABLE organizations DROP COLUMN created_by;

-- === organization_members.added_by → actor_id ===
ALTER TABLE organization_members ADD COLUMN IF NOT EXISTS actor_id BIGINT;

UPDATE organization_members SET actor_id = a.id
FROM actors a
WHERE a.user_id = organization_members.added_by
  AND a.kind = 'user'
  AND organization_members.actor_id IS NULL;

UPDATE organization_members SET actor_id = 1 WHERE actor_id IS NULL;

ALTER TABLE organization_members ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE organization_members ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;
ALTER TABLE organization_members DROP COLUMN added_by;

-- +goose Down
ALTER TABLE organization_members ADD COLUMN IF NOT EXISTS added_by BIGINT;
UPDATE organization_members SET added_by = a.user_id
FROM actors a
WHERE a.id = organization_members.actor_id AND a.kind = 'user';
ALTER TABLE organization_members DROP COLUMN IF EXISTS actor_id;

ALTER TABLE organizations ADD COLUMN IF NOT EXISTS created_by BIGINT;
UPDATE organizations SET created_by = a.user_id
FROM actors a
WHERE a.id = organizations.actor_id AND a.kind = 'user';
ALTER TABLE organizations DROP COLUMN IF EXISTS actor_id;
