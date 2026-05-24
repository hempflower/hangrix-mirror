
-- name: ExportUsageCSV :many
-- Page through the usage log in small chunks so the zip writer can stream
-- rows without holding the whole export in memory.
SELECT u.id, u.session_id, u.provider_id, u.model,
       u.prompt_tokens, u.completion_tokens, u.total_tokens,
       u.latency_ms, u.status_code, u.error_message, u.request_path,
       u.created_at,
       p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
ORDER BY u.created_at DESC
LIMIT sqlc.arg('lim')
OFFSET sqlc.arg('off');

-- name: ExportUsageJSONL :many
-- Page through the full usage log in small chunks so JSONL export can
-- stream request_body / response_body without loading the whole table.
SELECT u.id, u.session_id, u.provider_id, u.model,
       u.prompt_tokens, u.completion_tokens, u.total_tokens,
       u.latency_ms, u.status_code, u.error_message, u.request_path,
       u.created_at, u.request_body, u.response_body,
       p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
ORDER BY u.created_at DESC
LIMIT sqlc.arg('lim')
OFFSET sqlc.arg('off');

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
    disabled          = sqlc.arg('disabled'),
    updated_at        = NOW()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: SetProviderDisabled :one
-- Dedicated flip so the admin UI can toggle enable/disable without having
-- to round-trip the full UpdateProvider payload (and without risking a
-- stale base_url / allowed_models clobber).
UPDATE llm_providers SET
    disabled   = sqlc.arg('disabled'),
    updated_at = NOW()
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
-- array — without it the inferred type matches the column type. Disabled
-- providers are filtered out so the proxy responds 404 instead of routing
-- to an upstream the operator has paused.
SELECT * FROM llm_providers
WHERE sqlc.arg('model')::TEXT = ANY(allowed_models)
  AND NOT disabled
ORDER BY id ASC
LIMIT 1;

-- name: RecordUsage :exec
INSERT INTO llm_usage_log (
    session_id, provider_id, model,
    prompt_tokens, completion_tokens, total_tokens,
    latency_ms, status_code, error_message, request_path,
    request_body, response_body
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
    sqlc.arg('request_path'),
    sqlc.arg('request_body'),
    sqlc.arg('response_body')
);

-- name: ListUsage :many
-- Explicit column list excludes request_body/response_body so the list
-- query stays fast — the detail endpoint (GetUsageByID) carries the large
-- body columns on a single row.
SELECT u.id, u.session_id, u.provider_id, u.model,
       u.prompt_tokens, u.completion_tokens, u.total_tokens,
       u.latency_ms, u.status_code, u.error_message, u.request_path,
       u.created_at,
       p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
ORDER BY u.created_at DESC
LIMIT sqlc.arg('lim')
OFFSET sqlc.arg('off');

-- name: CountUsage :one
-- Mirrors ListUsage's WHERE clause so the admin usage page can render the
-- total row count alongside the paged window.
SELECT COUNT(*)::BIGINT
FROM llm_usage_log u
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'));

-- name: GetUsageByID :one
-- Single-row detail query that includes the large body columns the list
-- endpoint deliberately omits. Used by the admin detail popup.
SELECT u.*, p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE u.id = sqlc.arg('id');

-- name: DeleteUsageBefore :execrows
-- Hard-deletes usage-log rows whose created_at is strictly before :cutoff.
-- Called by the background reaper (service/reaper.go); not exposed through
-- the domain.Repo interface or the admin handler.
DELETE FROM llm_usage_log WHERE created_at < sqlc.arg('cutoff');

