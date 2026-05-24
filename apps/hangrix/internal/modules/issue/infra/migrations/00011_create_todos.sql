-- +goose Up
CREATE TABLE todos (
    id         BIGSERIAL PRIMARY KEY,
    issue_id   BIGINT    NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    content    TEXT      NOT NULL,
    status     TEXT      NOT NULL DEFAULT 'todo',
    position   INT       NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT todos_status_check CHECK (status IN ('todo', 'in_progress', 'done'))
);

CREATE INDEX idx_todos_issue_id ON todos(issue_id, position);

-- +goose Down
DROP TABLE IF EXISTS todos;
