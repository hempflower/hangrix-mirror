-- +goose Up
-- Platform-level attachments table. Unlike issue_attachments (which is scoped
-- to a repo+issue), these attachments live at the platform level and are
-- referenced from comments via native Markdown URLs (/api/attachments/{id}).
-- The human-vs-agent authorship XOR constraint matches the pattern used by
-- issue_comments and issue_attachments.
CREATE TABLE attachments (
    id                 BIGSERIAL PRIMARY KEY,
    storage_key        TEXT        NOT NULL UNIQUE,
    original_name      TEXT        NOT NULL,
    display_name       TEXT        NOT NULL DEFAULT '',
    size_bytes         BIGINT      NOT NULL,
    mime_type          TEXT        NOT NULL,
    detected_mime_type TEXT        NOT NULL DEFAULT '',
    sha256             TEXT        NOT NULL,
    kind               TEXT        NOT NULL CHECK (kind IN ('image','video','archive','text','binary')),
    inline             BOOLEAN     NOT NULL DEFAULT false,
    status             TEXT        NOT NULL CHECK (status IN ('uploaded','attached','deleted')) DEFAULT 'uploaded',
    author_id          BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    agent_role         TEXT        NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at         TIMESTAMPTZ,
    -- Human-vs-agent XOR: same pattern as issue_comments and issue_attachments.
    CONSTRAINT attachments_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    )
);

CREATE INDEX idx_attachments_status ON attachments(status);

-- Junction table linking comments to platform-level attachments. A single
-- comment can reference multiple attachments and vice versa.
CREATE TABLE comment_attachments (
    comment_id    BIGINT NOT NULL REFERENCES issue_comments(id) ON DELETE CASCADE,
    attachment_id BIGINT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
    PRIMARY KEY (comment_id, attachment_id)
);

CREATE INDEX idx_comment_attachments_attachment ON comment_attachments(attachment_id);

-- +goose Down
DROP INDEX IF EXISTS idx_comment_attachments_attachment;
DROP TABLE IF EXISTS comment_attachments;
DROP INDEX IF EXISTS idx_attachments_status;
DROP TABLE IF EXISTS attachments;
