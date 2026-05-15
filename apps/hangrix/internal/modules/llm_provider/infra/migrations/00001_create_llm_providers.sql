-- +goose Up

-- llm_providers is the platform-wide registry of upstream LLM endpoints.
-- api_key_encrypted holds the cryptobox sealed blob (base64(nonce||ct||tag));
-- only the proxy ever decrypts it.
--
-- allowed_models is now load-bearing for routing — the proxy scans this
-- column to pick the upstream that should serve an incoming `model`. A
-- row with an empty array participates in no routing decisions.
CREATE TABLE llm_providers (
    id                  BIGSERIAL PRIMARY KEY,
    name                TEXT        NOT NULL UNIQUE,
    type                TEXT        NOT NULL CHECK (type IN ('openai', 'anthropic', 'openai-compat')),
    base_url            TEXT        NOT NULL DEFAULT '',
    api_key_encrypted   TEXT        NOT NULL,
    allowed_models      TEXT[]      NOT NULL DEFAULT '{}',
    created_by          BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- GIN index over allowed_models so FindProviderByModel can answer
-- "which provider lists this model?" without a sequential scan as the
-- registry grows.
CREATE INDEX llm_providers_allowed_models_idx
    ON llm_providers USING GIN (allowed_models);

-- llm_usage_log captures one row per proxy round-trip plus error attempts.
-- session_id is the agent_sessions.id the caller authenticated as (when
-- the request carried a session-token bearer). It is stored as a plain
-- BIGINT — no FK — so this module stays decoupled from modules/runner
-- (the two modules manage their own migration timelines).
CREATE TABLE llm_usage_log (
    id                BIGSERIAL PRIMARY KEY,
    session_id        BIGINT,
    provider_id       BIGINT      NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
    model             TEXT        NOT NULL,
    prompt_tokens     INTEGER     NOT NULL DEFAULT 0,
    completion_tokens INTEGER     NOT NULL DEFAULT 0,
    total_tokens      INTEGER     NOT NULL DEFAULT 0,
    latency_ms        INTEGER     NOT NULL DEFAULT 0,
    status_code       INTEGER     NOT NULL DEFAULT 0,
    error_message     TEXT        NOT NULL DEFAULT '',
    request_path      TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX llm_usage_log_provider_time_idx ON llm_usage_log (provider_id, created_at DESC);
CREATE INDEX llm_usage_log_session_idx       ON llm_usage_log (session_id) WHERE session_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS llm_usage_log_session_idx;
DROP INDEX IF EXISTS llm_usage_log_provider_time_idx;
DROP TABLE IF EXISTS llm_usage_log;

DROP INDEX IF EXISTS llm_providers_allowed_models_idx;
DROP TABLE IF EXISTS llm_providers;
