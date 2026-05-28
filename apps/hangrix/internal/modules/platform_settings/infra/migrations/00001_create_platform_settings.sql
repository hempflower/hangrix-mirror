-- +goose Up

CREATE TABLE IF NOT EXISTS platform_settings (
    key         TEXT        PRIMARY KEY,
    value       TEXT        NOT NULL DEFAULT '',
    description TEXT        NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down

DROP TABLE IF EXISTS platform_settings;
