-- +goose Up
CREATE TABLE branch_protections (
    id                  BIGSERIAL PRIMARY KEY,
    repo_id             BIGINT      NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    pattern             TEXT        NOT NULL,
    forbid_force_push   BOOLEAN     NOT NULL DEFAULT TRUE,
    forbid_delete       BOOLEAN     NOT NULL DEFAULT TRUE,
    forbid_direct_push  BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (repo_id, pattern)
);

CREATE INDEX idx_branch_protections_repo ON branch_protections(repo_id);

-- +goose Down
DROP INDEX IF EXISTS idx_branch_protections_repo;
DROP TABLE branch_protections;
