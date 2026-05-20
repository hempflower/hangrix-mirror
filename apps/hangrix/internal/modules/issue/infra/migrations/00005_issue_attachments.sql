-- +goose Up
-- Attachments belong to an issue and (optionally) a comment. They use the
-- same human-vs-agent authorship model as issue_comments: author_id +
-- agent_role are mutually exclusive via the CHECK constraint.
CREATE TABLE issue_attachments (
    id                 BIGSERIAL PRIMARY KEY,
    repo_id            BIGINT      NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    issue_id           BIGINT      NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    comment_id         BIGINT      REFERENCES issue_comments(id) ON DELETE SET NULL,
    author_id          BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    agent_role         TEXT        NOT NULL DEFAULT '',
    storage_key        TEXT        NOT NULL UNIQUE,
    original_name      TEXT        NOT NULL,
    size_bytes         BIGINT      NOT NULL,
    mime_type          TEXT        NOT NULL,
    detected_mime_type TEXT        NOT NULL DEFAULT '',
    sha256             TEXT        NOT NULL,
    kind               TEXT        NOT NULL CHECK (kind IN ('image','video','archive','text','binary')),
    status             TEXT        NOT NULL CHECK (status IN ('uploaded','attached','deleted')) DEFAULT 'uploaded',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at         TIMESTAMPTZ,
    -- Human-vs-agent XOR: same pattern as issue_comments and issues.
    CONSTRAINT issue_attachments_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    )
);

CREATE INDEX idx_issue_attachments_issue ON issue_attachments(issue_id, status);
CREATE INDEX idx_issue_attachments_comment ON issue_attachments(comment_id) WHERE comment_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_issue_attachments_comment;
DROP INDEX IF EXISTS idx_issue_attachments_issue;
DROP TABLE IF EXISTS issue_attachments;
