-- name: CreateRepoForUser :one
INSERT INTO repos (owner_user_id, name, description, visibility, default_branch)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreateRepoForOrg :one
INSERT INTO repos (owner_org_id, name, description, visibility, default_branch)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetRepoByID :one
SELECT r.*,
       COALESCE(u.username, o.name) AS owner_name,
       CASE
           WHEN r.owner_user_id IS NOT NULL THEN 'user'
           ELSE 'org'
       END AS owner_kind
FROM repos r
LEFT JOIN users u         ON u.id = r.owner_user_id
LEFT JOIN organizations o ON o.id = r.owner_org_id AND o.deleted_at IS NULL
WHERE r.id = $1;

-- name: GetRepoByUserOwnerAndName :one
SELECT r.*,
       u.username AS owner_name,
       'user'::text AS owner_kind
FROM repos r
JOIN users u ON u.id = r.owner_user_id
WHERE r.owner_user_id = $1 AND r.name = $2;

-- name: GetRepoByOrgOwnerAndName :one
SELECT r.*,
       o.name AS owner_name,
       'org'::text AS owner_kind
FROM repos r
JOIN organizations o ON o.id = r.owner_org_id
WHERE r.owner_org_id = $1 AND r.name = $2 AND o.deleted_at IS NULL;

-- name: ListReposByUserOwner :many
SELECT r.*,
       u.username AS owner_name,
       'user'::text AS owner_kind
FROM repos r
JOIN users u ON u.id = r.owner_user_id
WHERE r.owner_user_id = $1
  AND (sqlc.arg(include_private)::bool OR r.visibility = 'public')
  AND (sqlc.narg('kind')::TEXT IS NULL OR r.kind = sqlc.narg('kind'))
ORDER BY r.created_at DESC, r.id DESC
LIMIT $2 OFFSET $3;

-- name: CountReposByUserOwner :one
SELECT COUNT(*)
FROM repos r
WHERE r.owner_user_id = $1
  AND (sqlc.arg(include_private)::bool OR r.visibility = 'public')
  AND (sqlc.narg('kind')::TEXT IS NULL OR r.kind = sqlc.narg('kind'));

-- name: ListReposByOrgOwner :many
SELECT r.*,
       o.name AS owner_name,
       'org'::text AS owner_kind
FROM repos r
JOIN organizations o ON o.id = r.owner_org_id
WHERE r.owner_org_id = $1
  AND o.deleted_at IS NULL
  AND (sqlc.arg(include_private)::bool OR r.visibility = 'public')
  AND (sqlc.narg('kind')::TEXT IS NULL OR r.kind = sqlc.narg('kind'))
ORDER BY r.created_at DESC, r.id DESC
LIMIT $2 OFFSET $3;

-- name: CountReposByOrgOwner :one
SELECT COUNT(*)
FROM repos r
JOIN organizations o ON o.id = r.owner_org_id
WHERE r.owner_org_id = $1
  AND o.deleted_at IS NULL
  AND (sqlc.arg(include_private)::bool OR r.visibility = 'public')
  AND (sqlc.narg('kind')::TEXT IS NULL OR r.kind = sqlc.narg('kind'));

-- name: UpdateRepoKind :execrows
-- Called by the receive-pack post-receive observer when the default branch
-- advances. Idempotent and cheap; the index on kind='agent' means the
-- transition itself triggers an index update only when kind actually
-- changes. Validation of the agent.yml content lives in the service
-- layer (agents_config parser); this query is dumb.
UPDATE repos
SET kind = sqlc.arg('kind'), updated_at = NOW()
WHERE id = sqlc.arg('id') AND kind IS DISTINCT FROM sqlc.arg('kind');

-- name: DeleteRepo :execrows
DELETE FROM repos WHERE id = $1;

-- name: UpdateRepoMeta :one
UPDATE repos
SET description    = $2,
    default_branch = $3,
    visibility     = $4,
    updated_at     = NOW()
WHERE id = $1
RETURNING *;

-- name: TransferRepoToUser :execrows
UPDATE repos
SET owner_user_id = $2,
    owner_org_id  = NULL,
    updated_at    = NOW()
WHERE id = $1;

-- name: TransferRepoToOrg :execrows
UPDATE repos
SET owner_user_id = NULL,
    owner_org_id  = $2,
    updated_at    = NOW()
WHERE id = $1;

-- name: ListBranchProtectionsByRepo :many
SELECT *
FROM branch_protections
WHERE repo_id = $1
ORDER BY pattern;

-- name: GetBranchProtection :one
SELECT *
FROM branch_protections
WHERE id = $1 AND repo_id = $2;

-- name: CreateBranchProtection :one
INSERT INTO branch_protections (repo_id, pattern, forbid_force_push, forbid_delete, forbid_direct_push)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateBranchProtection :one
UPDATE branch_protections
SET pattern             = $3,
    forbid_force_push   = $4,
    forbid_delete       = $5,
    forbid_direct_push  = $6,
    updated_at          = NOW()
WHERE id = $1 AND repo_id = $2
RETURNING *;

-- name: DeleteBranchProtection :execrows
DELETE FROM branch_protections WHERE id = $1 AND repo_id = $2;
