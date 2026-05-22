-- +goose Up
-- issue_patches stores patch submissions for the patch-based contribution
-- workflow (issue #102). Each row is a single unified diff submitted by an
-- agent session against a specific issue.
CREATE TABLE issue_patches (
    id              BIGSERIAL PRIMARY KEY,
    repo_id         BIGINT  NOT NULL REFERENCES repos(id),
    issue_id        BIGINT  NOT NULL REFERENCES issues(id),
    session_id      BIGINT  NOT NULL,
    agent_role      TEXT    NOT NULL,
    base_head_sha   TEXT    NOT NULL,
    title           TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    patch_text      TEXT    NOT NULL,
    changed_paths   TEXT[]  NOT NULL DEFAULT '{}',
    file_count      INT     NOT NULL DEFAULT 0,
    additions       INT     NOT NULL DEFAULT 0,
    deletions       INT     NOT NULL DEFAULT 0,
    status          TEXT    NOT NULL DEFAULT 'submitted'
                          CHECK (status IN ('submitted','stale','applied','rejected','superseded')),
    applied_commit_sha TEXT,
    applied_at        TIMESTAMPTZ,
    rejected_reason   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_issue_patches_issue_id ON issue_patches(issue_id);
CREATE INDEX idx_issue_patches_status ON issue_patches(status);
CREATE INDEX idx_issue_patches_session_id ON issue_patches(session_id);
CREATE INDEX idx_issue_patches_created_at ON issue_patches(created_at);

-- +goose Down
DROP TABLE IF EXISTS issue_patches;
