-- +goose Up
CREATE TABLE repo_members (
    repo_id   BIGINT      NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    user_id   BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role      TEXT        NOT NULL CHECK (role IN ('read', 'write')) DEFAULT 'read',
    added_by  BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (repo_id, user_id)
);

CREATE INDEX idx_repo_members_user ON repo_members(user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_repo_members_user;
DROP TABLE repo_members;
