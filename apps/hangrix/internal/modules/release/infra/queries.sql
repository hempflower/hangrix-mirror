-- name: CreateRelease :one
INSERT INTO releases (repo_id, tag_name, target_commit_sha, title, notes, is_draft,
    created_actor_kind, created_actor_user_id, created_actor_role_key, created_actor_workflow_run_id, created_actor_display_name)
VALUES ($1, $2, $3, $4, $5, $6,
    $7, sqlc.narg('created_actor_user_id'), $8, sqlc.narg('created_actor_workflow_run_id'), $9)
RETURNING *;

-- name: GetReleaseByID :one
SELECT * FROM releases WHERE id = $1;

-- name: GetReleaseByRepoAndTag :one
SELECT * FROM releases WHERE repo_id = $1 AND tag_name = $2;

-- name: ListReleasesByRepo :many
SELECT * FROM releases
WHERE repo_id = $1
ORDER BY published_at DESC NULLS LAST, created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountReleasesByRepo :one
SELECT COUNT(*) FROM releases WHERE repo_id = $1;

-- name: ListReleasesByRepoDraft :many
SELECT * FROM releases
WHERE repo_id = $1 AND is_draft = $2
ORDER BY published_at DESC NULLS LAST, created_at DESC
LIMIT $3 OFFSET $4;

-- name: CountReleasesByRepoDraft :one
SELECT COUNT(*) FROM releases WHERE repo_id = $1 AND is_draft = $2;

-- name: UpdateRelease :one
UPDATE releases
SET tag_name          = $2,
    target_commit_sha = $3,
    title             = $4,
    notes             = $5,
    updated_at        = NOW()
WHERE id = $1
RETURNING *;

-- name: PublishRelease :one
UPDATE releases
SET is_draft    = FALSE,
    published_at = NOW(),
    updated_at   = NOW(),
    published_actor_kind = $2,
    published_actor_user_id = sqlc.narg('published_actor_user_id'),
    published_actor_role_key = $3,
    published_actor_workflow_run_id = sqlc.narg('published_actor_workflow_run_id'),
    published_actor_display_name = $4
WHERE id = $1 AND is_draft = TRUE
RETURNING *;

-- name: DeleteRelease :execrows
DELETE FROM releases WHERE id = $1;

-- name: CreateAsset :one
INSERT INTO release_assets (release_id, name, content_type, size_bytes, storage_key,
    uploaded_actor_kind, uploaded_actor_user_id, uploaded_actor_role_key, uploaded_actor_workflow_run_id, uploaded_actor_display_name)
VALUES ($1, $2, $3, $4, $5,
    $6, sqlc.narg('uploaded_actor_user_id'), $7, sqlc.narg('uploaded_actor_workflow_run_id'), $8)
RETURNING *;

-- name: GetAssetByID :one
SELECT * FROM release_assets WHERE id = $1;

-- name: GetAssetByReleaseAndName :one
SELECT * FROM release_assets WHERE release_id = $1 AND name = $2;

-- name: ListAssetsByRelease :many
SELECT * FROM release_assets
WHERE release_id = $1
ORDER BY name;

-- name: DeleteAsset :execrows
DELETE FROM release_assets WHERE id = $1;
