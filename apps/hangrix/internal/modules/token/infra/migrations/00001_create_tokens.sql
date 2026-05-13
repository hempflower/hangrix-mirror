-- +goose Up
CREATE TABLE access_tokens (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT        NOT NULL,
    prefix        TEXT        NOT NULL UNIQUE,
    hashed_key    TEXT        NOT NULL,
    scopes        TEXT[]      NOT NULL DEFAULT '{}',
    last_used_at  TIMESTAMPTZ,
    expires_at    TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX access_tokens_user_id_idx ON access_tokens (user_id);

-- +goose Down
DROP INDEX access_tokens_user_id_idx;
DROP TABLE access_tokens;
