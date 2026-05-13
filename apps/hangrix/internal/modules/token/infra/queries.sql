-- name: CreateToken :one
INSERT INTO access_tokens (user_id, name, prefix, hashed_key, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetTokenByPrefix :one
SELECT * FROM access_tokens WHERE prefix = $1;

-- name: ListTokensByUser :many
SELECT * FROM access_tokens
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: RevokeToken :execrows
UPDATE access_tokens
SET revoked_at = NOW()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: TouchTokenLastUsed :exec
UPDATE access_tokens
SET last_used_at = NOW()
WHERE id = $1;
