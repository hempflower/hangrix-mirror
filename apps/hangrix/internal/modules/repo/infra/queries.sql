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
