-- name: SummaryStats :one
SELECT
  COUNT(*)::BIGINT AS total_calls,
  COALESCE(SUM(total_tokens), 0)::BIGINT AS total_tokens,
  COUNT(*) FILTER (WHERE status_code >= 400)::BIGINT AS failed_calls
FROM llm_usage_log u
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::TIMESTAMPTZ IS NULL OR u.created_at < sqlc.narg('until'));

-- name: DailyCalls :many
SELECT
  DATE(created_at)::TEXT AS date,
  COUNT(*)::BIGINT AS count
FROM llm_usage_log u
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::TIMESTAMPTZ IS NULL OR u.created_at < sqlc.narg('until'))
GROUP BY DATE(created_at)
ORDER BY DATE(created_at) ASC;

-- name: DailyTokens :many
SELECT
  DATE(created_at)::TEXT AS date,
  COALESCE(SUM(total_tokens), 0)::BIGINT AS total_tokens,
  COALESCE(SUM(prompt_tokens), 0)::BIGINT AS prompt_tokens,
  COALESCE(SUM(completion_tokens), 0)::BIGINT AS completion_tokens
FROM llm_usage_log u
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::TIMESTAMPTZ IS NULL OR u.created_at < sqlc.narg('until'))
GROUP BY DATE(created_at)
ORDER BY DATE(created_at) ASC;

-- name: ProviderBreakdown :many
SELECT
  p.name AS provider_name,
  COUNT(*)::BIGINT AS calls,
  COALESCE(SUM(u.total_tokens), 0)::BIGINT AS total_tokens
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::TIMESTAMPTZ IS NULL OR u.created_at < sqlc.narg('until'))
GROUP BY p.id, p.name
ORDER BY calls DESC;

-- name: RunnerHealth :one
SELECT
  COUNT(*) FILTER (WHERE status = 'active' AND last_heartbeat_at >= NOW() - INTERVAL '60 seconds')::BIGINT AS online_runners,
  COUNT(*) FILTER (WHERE status = 'active' AND (last_heartbeat_at IS NULL OR last_heartbeat_at < NOW() - INTERVAL '60 seconds'))::BIGINT AS offline_runners,
  COUNT(*) FILTER (WHERE status = 'disabled')::BIGINT AS disabled_runners,
  COUNT(*)::BIGINT AS total_runners
FROM runners;

-- name: LiveSessions :one
SELECT COUNT(*)::BIGINT AS live_sessions
FROM agent_sessions
WHERE status IN ('pending', 'claimed', 'running', 'idle');

-- name: RecentFailures :many
SELECT
  u.id,
  p.name AS provider_name,
  u.model,
  u.status_code,
  u.error_message,
  u.created_at,
  u.session_id
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE u.status_code >= 400
  AND (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::TIMESTAMPTZ IS NULL OR u.created_at < sqlc.narg('until'))
ORDER BY u.created_at DESC
LIMIT sqlc.arg('lim');
