-- name: GetSetting :one
SELECT key, value, description, updated_at
FROM platform_settings
WHERE key = sqlc.arg('key');

-- name: UpsertSetting :exec
INSERT INTO platform_settings (key, value, description)
VALUES (
    sqlc.arg('key'),
    sqlc.arg('value'),
    sqlc.arg('description')
)
ON CONFLICT (key) DO UPDATE SET
    value      = EXCLUDED.value,
    updated_at = NOW();

-- name: ListSettings :many
SELECT key, value, description, updated_at
FROM platform_settings
ORDER BY key ASC;
