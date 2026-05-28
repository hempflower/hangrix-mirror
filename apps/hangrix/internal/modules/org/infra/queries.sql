-- name: CreateOrganization :one
INSERT INTO organizations (name, display_name, description, actor_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetOrganizationByID :one
SELECT *
FROM organizations
WHERE id = $1 AND deleted_at IS NULL;

-- name: GetOrganizationByName :one
SELECT *
FROM organizations
WHERE name = $1 AND deleted_at IS NULL;

-- name: ExistsOrganizationName :one
SELECT EXISTS (
    SELECT 1 FROM organizations WHERE name = $1 AND deleted_at IS NULL
);

-- name: UpdateOrganizationMeta :one
UPDATE organizations
SET display_name = $2,
    description  = $3,
    avatar_url   = $4,
    updated_at   = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteOrganization :execrows
UPDATE organizations
SET deleted_at = NOW(),
    updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL;

-- name: AddOrganizationMember :exec
INSERT INTO organization_members (org_id, user_id, role, actor_id)
VALUES ($1, $2, $3, $4);

-- name: UpdateOrganizationMemberRole :execrows
UPDATE organization_members
SET role = $3
WHERE org_id = $1 AND user_id = $2;

-- name: RemoveOrganizationMember :execrows
DELETE FROM organization_members
WHERE org_id = $1 AND user_id = $2;

-- name: GetOrganizationMember :one
SELECT m.org_id, m.user_id, u.username, m.role, m.actor_id, m.added_at
FROM organization_members m
JOIN users u ON u.id = m.user_id
WHERE m.org_id = $1 AND m.user_id = $2;

-- name: ListOrganizationMembers :many
SELECT m.org_id, m.user_id, u.username, m.role, m.actor_id, m.added_at
FROM organization_members m
JOIN users u ON u.id = m.user_id
WHERE m.org_id = $1
ORDER BY m.role DESC, u.username ASC;

-- name: CountOrganizationOwners :one
SELECT COUNT(*) FROM organization_members
WHERE org_id = $1 AND role = 'owner';

-- name: ListOrganizationsForUser :many
SELECT o.*
FROM organizations o
JOIN organization_members m ON m.org_id = o.id
WHERE m.user_id = $1 AND o.deleted_at IS NULL
ORDER BY o.name ASC;
