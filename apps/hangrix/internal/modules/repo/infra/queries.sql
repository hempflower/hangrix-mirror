-- name: CreateRepo :one
INSERT INTO repos (owner_id, name, description, visibility, default_branch)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetRepoByID :one
SELECT r.*, u.username AS owner_username
FROM repos r
JOIN users u ON u.id = r.owner_id
WHERE r.id = $1;

-- name: GetRepoByOwnerAndName :one
SELECT r.*, u.username AS owner_username
FROM repos r
JOIN users u ON u.id = r.owner_id
WHERE r.owner_id = $1 AND r.name = $2;

-- name: ListReposByOwner :many
SELECT r.*, u.username AS owner_username
FROM repos r
JOIN users u ON u.id = r.owner_id
WHERE r.owner_id = $1
  AND (sqlc.arg(include_private)::bool OR r.visibility = 'public')
ORDER BY r.created_at DESC, r.id DESC
LIMIT $2 OFFSET $3;

-- name: CountReposByOwner :one
SELECT COUNT(*)
FROM repos r
WHERE r.owner_id = $1
  AND (sqlc.arg(include_private)::bool OR r.visibility = 'public');

-- name: ExistsRepoNameForOwner :one
SELECT EXISTS (
    SELECT 1 FROM repos WHERE owner_id = $1 AND name = $2
);

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
