-- +goose Up
-- Baseline migration. IF NOT EXISTS is intentional because some local DBs
-- predate the migration system and already have a `users` table created by
-- the previous startup-bootstrap script; this lets goose record version 1 as
-- applied without crashing on those DBs. Future migrations MUST NOT use
-- IF [NOT] EXISTS guards.
CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT        NOT NULL UNIQUE,
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL CHECK (role IN ('user', 'admin')) DEFAULT 'user',
    disabled      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE users;
