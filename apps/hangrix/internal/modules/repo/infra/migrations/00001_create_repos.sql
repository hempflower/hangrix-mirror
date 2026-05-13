-- +goose Up
CREATE TABLE repos (
    id              BIGSERIAL PRIMARY KEY,
    owner_id        BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    visibility      TEXT        NOT NULL CHECK (visibility IN ('public', 'private')) DEFAULT 'private',
    default_branch  TEXT        NOT NULL DEFAULT 'main',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_id, name)
);

-- +goose Down
DROP TABLE repos;
