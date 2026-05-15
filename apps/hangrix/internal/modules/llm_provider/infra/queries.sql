-- name: CreateProvider :one
INSERT INTO llm_providers (
    name, type, base_url, api_key_encrypted, allowed_models, created_by
) VALUES (
    sqlc.arg('name'),
    sqlc.arg('type'),
    sqlc.arg('base_url'),
    sqlc.arg('api_key_encrypted'),
    sqlc.arg('allowed_models'),
    sqlc.arg('created_by')
)
RETURNING *;

-- name: UpdateProvider :one
-- sqlc.narg('api_key_encrypted') makes the parameter nullable; COALESCE
-- keeps the stored sealed blob when the caller passes NULL (= no rotation).
UPDATE llm_providers SET
    base_url          = sqlc.arg('base_url'),
    api_key_encrypted = COALESCE(sqlc.narg('api_key_encrypted'), api_key_encrypted),
    allowed_models    = sqlc.arg('allowed_models'),
    updated_at        = NOW()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: GetProviderByID :one
SELECT * FROM llm_providers WHERE id = sqlc.arg('id');

-- name: GetProviderByName :one
SELECT * FROM llm_providers WHERE name = sqlc.arg('name');

-- name: ListProviders :many
SELECT * FROM llm_providers ORDER BY name ASC;

-- name: DeleteProvider :execrows
DELETE FROM llm_providers WHERE id = sqlc.arg('id');

-- name: FindProviderByModel :one
-- Lowest-id provider whose allowed_models contains :model wins. Explicit
-- ::TEXT cast tells sqlc that the parameter is a single string, not an
-- array — without it the inferred type matches the column type.
SELECT * FROM llm_providers
WHERE sqlc.arg('model')::TEXT = ANY(allowed_models)
ORDER BY id ASC
LIMIT 1;

-- name: RecordUsage :exec
INSERT INTO llm_usage_log (
    session_id, provider_id, model,
    prompt_tokens, completion_tokens, total_tokens,
    latency_ms, status_code, error_message, request_path
) VALUES (
    sqlc.narg('session_id'),
    sqlc.arg('provider_id'),
    sqlc.arg('model'),
    sqlc.arg('prompt_tokens'),
    sqlc.arg('completion_tokens'),
    sqlc.arg('total_tokens'),
    sqlc.arg('latency_ms'),
    sqlc.arg('status_code'),
    sqlc.arg('error_message'),
    sqlc.arg('request_path')
);

-- name: ListUsage :many
SELECT u.*, p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
ORDER BY u.created_at DESC
LIMIT sqlc.arg('lim');
