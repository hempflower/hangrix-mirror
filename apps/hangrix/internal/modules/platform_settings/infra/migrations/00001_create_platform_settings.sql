-- +goose Up
CREATE TABLE platform_settings (
  key         TEXT PRIMARY KEY,
  value       TEXT NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_by  BIGINT REFERENCES users(id) ON DELETE SET NULL
);

-- +goose Down
DROP TABLE IF EXISTS platform_settings;
