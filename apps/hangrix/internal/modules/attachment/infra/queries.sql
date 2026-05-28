-- ---- attachments (platform-level) ----

-- name: CreateAttachment :one
INSERT INTO attachments (
    storage_key, original_name, display_name, size_bytes,
    mime_type, detected_mime_type, sha256, kind, inline,
    status, actor_id
)
VALUES (
    sqlc.arg('storage_key'),
    sqlc.arg('original_name'),
    sqlc.arg('display_name'),
    sqlc.arg('size_bytes'),
    sqlc.arg('mime_type'),
    sqlc.arg('detected_mime_type'),
    sqlc.arg('sha256'),
    sqlc.arg('kind'),
    sqlc.arg('inline'),
    sqlc.arg('status'),
    sqlc.arg('actor_id')
)
RETURNING id, created_at;

-- name: GetAttachment :one
SELECT a.id, a.storage_key, a.original_name, a.display_name,
       a.size_bytes, a.mime_type, a.detected_mime_type, a.sha256,
       a.kind, a.inline, a.status,
       a.actor_id, a.created_at, a.deleted_at
FROM attachments a
WHERE a.id = sqlc.arg('id');

-- name: SoftDeleteAttachment :exec
UPDATE attachments
SET status = 'deleted', deleted_at = NOW()
WHERE id = sqlc.arg('id') AND status <> 'deleted';

-- ---- comment_attachments (junction) ----

-- name: CreateCommentAttachment :exec
INSERT INTO comment_attachments (comment_id, attachment_id)
VALUES (sqlc.arg('comment_id'), sqlc.arg('attachment_id'))
ON CONFLICT DO NOTHING;

-- name: ListAttachmentIDsByComment :many
SELECT attachment_id
FROM comment_attachments
WHERE comment_id = sqlc.arg('comment_id')
ORDER BY attachment_id;
