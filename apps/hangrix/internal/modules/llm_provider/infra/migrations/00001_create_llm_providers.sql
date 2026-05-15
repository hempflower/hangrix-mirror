-- +goose Up

-- llm_providers is the platform-wide registry of upstream LLM endpoints.
-- api_key_encrypted holds the cryptobox sealed blob (base64(nonce||ct||tag));
-- only the proxy ever decrypts it. The partial unique index enforces the
-- "at most one platform default" invariant at the DB level so concurrent
-- toggles can't both win.
CREATE TABLE llm_providers (
    id                  BIGSERIAL PRIMARY KEY,
    name                TEXT        NOT NULL UNIQUE,
    type                TEXT        NOT NULL CHECK (type IN ('openai', 'anthropic', 'openai-compat')),
    base_url            TEXT        NOT NULL DEFAULT '',
    api_key_encrypted   TEXT        NOT NULL,
    allowed_models      TEXT[]      NOT NULL DEFAULT '{}',
    visibility          TEXT        NOT NULL CHECK (visibility IN ('platform', 'restricted')) DEFAULT 'platform',
    allowed_repos       TEXT[]      NOT NULL DEFAULT '{}',
    rate_limit_rpm      INTEGER     NOT NULL DEFAULT 0,
    is_platform_default BOOLEAN     NOT NULL DEFAULT FALSE,
    default_model       TEXT        NOT NULL DEFAULT '',
    created_by          BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX llm_providers_one_default
    ON llm_providers (is_platform_default)
    WHERE is_platform_default = TRUE;

-- llm_session_tokens carry the `hgxs_<prefix>_<secret>` artefacts the proxy
-- expects in Authorization: Bearer. hashed_key is bcrypt(secret); the
-- plaintext is shown exactly once on creation. provider_id ON DELETE CASCADE
-- because a removed provider invalidates every token bound to it.
CREATE TABLE llm_session_tokens (
    id            BIGSERIAL PRIMARY KEY,
    prefix        TEXT        NOT NULL UNIQUE,
    hashed_key    TEXT        NOT NULL,
    provider_id   BIGINT      NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
    model         TEXT        NOT NULL,
    label         TEXT        NOT NULL DEFAULT '',
    created_by    BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    last_used_at  TIMESTAMPTZ,
    expires_at    TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX llm_session_tokens_provider_idx ON llm_session_tokens (provider_id);

-- llm_usage_log captures one row per successful proxy round-trip plus error
-- attempts. session_token_id is nullable + ON DELETE SET NULL so revoking or
-- pruning a token does not wipe historical usage rows.
CREATE TABLE llm_usage_log (
    id                BIGSERIAL PRIMARY KEY,
    session_token_id  BIGINT      REFERENCES llm_session_tokens(id) ON DELETE SET NULL,
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
CREATE INDEX llm_usage_log_session_idx       ON llm_usage_log (session_token_id);

-- +goose Down
DROP INDEX IF EXISTS llm_usage_log_session_idx;
DROP INDEX IF EXISTS llm_usage_log_provider_time_idx;
DROP TABLE IF EXISTS llm_usage_log;

DROP INDEX IF EXISTS llm_session_tokens_provider_idx;
DROP TABLE IF EXISTS llm_session_tokens;

DROP INDEX IF EXISTS llm_providers_one_default;
DROP TABLE IF EXISTS llm_providers;
