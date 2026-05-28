-- name: ListAll :many
SELECT key, value, updated_at, updated_by FROM platform_settings ORDER BY key ASC;

-- name: GetByKey :one
SELECT key, value, updated_at, updated_by FROM platform_settings WHERE key = sqlc.arg('key');

-- name: UpsertSetting :exec
INSERT INTO platform_settings (key, value, updated_by, updated_at)
VALUES (sqlc.arg('key'), sqlc.arg('value'), sqlc.narg('updated_by'), NOW())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_by = EXCLUDED.updated_by,
    updated_at = NOW();
