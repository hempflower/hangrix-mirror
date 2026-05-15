-- +goose Up

-- M5 — repositories may now belong to either a user or an organization.
-- Rename the existing owner_id column (formerly user-only) to owner_user_id,
-- relax its NOT NULL, then add owner_org_id pointing at organizations(id).
-- A CHECK guarantees exactly one of the two is non-NULL; partial uniques
-- preserve the (owner, name) uniqueness contract per owner kind.
ALTER TABLE repos RENAME COLUMN owner_id TO owner_user_id;
ALTER TABLE repos ALTER COLUMN owner_user_id DROP NOT NULL;

ALTER TABLE repos ADD COLUMN owner_org_id BIGINT NULL REFERENCES organizations(id) ON DELETE CASCADE;

-- Drop the old `(owner_id, name)` unique; recreate as partial uniques.
ALTER TABLE repos DROP CONSTRAINT repos_owner_id_name_key;
CREATE UNIQUE INDEX repos_owner_user_id_name_key
    ON repos (owner_user_id, name)
    WHERE owner_user_id IS NOT NULL;
CREATE UNIQUE INDEX repos_owner_org_id_name_key
    ON repos (owner_org_id, name)
    WHERE owner_org_id IS NOT NULL;

ALTER TABLE repos ADD CONSTRAINT repos_owner_xor CHECK (
    (owner_user_id IS NOT NULL AND owner_org_id IS NULL) OR
    (owner_user_id IS NULL AND owner_org_id IS NOT NULL)
);

-- +goose Down
ALTER TABLE repos DROP CONSTRAINT repos_owner_xor;
DROP INDEX IF EXISTS repos_owner_org_id_name_key;
DROP INDEX IF EXISTS repos_owner_user_id_name_key;
ALTER TABLE repos DROP COLUMN owner_org_id;
ALTER TABLE repos ALTER COLUMN owner_user_id SET NOT NULL;
ALTER TABLE repos ADD CONSTRAINT repos_owner_id_name_key UNIQUE (owner_user_id, name);
ALTER TABLE repos RENAME COLUMN owner_user_id TO owner_id;
