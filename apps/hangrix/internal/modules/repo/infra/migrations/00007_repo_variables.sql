-- +goose Up
CREATE TABLE repo_variables (
    id         BIGSERIAL    PRIMARY KEY,
    repo_id    BIGINT       NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name       TEXT         NOT NULL,
    value      TEXT         NOT NULL DEFAULT '',
    kind       TEXT         NOT NULL CHECK (kind IN ('plain', 'secret')),
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE (repo_id, name)
);

CREATE INDEX idx_repo_variables_repo ON repo_variables(repo_id);

-- +goose Down
DROP INDEX IF EXISTS idx_repo_variables_repo;
DROP TABLE repo_variables;
