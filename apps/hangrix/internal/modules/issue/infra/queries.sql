-- ---- issue_counters ----

-- name: NextIssueNumber :one
-- Atomic per-repo issue counter. RETURNS the value just claimed; the row
-- initialiser starts at 2 because the first claim must return 1.
-- Explicit BIGINT cast on the SELECT side so sqlc emits int64 rather
-- than narrowing to int32 on the `next - 1` arithmetic.
INSERT INTO issue_counters (repo_id, next)
VALUES (sqlc.arg('repo_id'), 2)
ON CONFLICT (repo_id) DO UPDATE SET next = issue_counters.next + 1
RETURNING (next - 1)::BIGINT AS number;

-- ---- issues ----

-- name: CreateIssue :one
-- author_id and agent_role are mutually exclusive (CHECK constraint).
-- Human path: sqlc.narg('author_id') with the user's ID, agent_role=''.
-- Agent path: author_id=NULL (omit), agent_role with the role key.
INSERT INTO issues (
    repo_id, number, author_id, agent_role, title, body, branch_name,
    base_branch, parent_id, parent_number
)
VALUES (
    sqlc.arg('repo_id'),
    sqlc.arg('number'),
    sqlc.narg('author_id'),
    sqlc.arg('agent_role'),
    sqlc.arg('title'),
    sqlc.arg('body'),
    sqlc.arg('branch_name'),
    sqlc.arg('base_branch'),
    sqlc.narg('parent_id'),
    sqlc.arg('parent_number')
)
RETURNING id, state, created_at, updated_at;

-- name: GetIssueByNumber :one
SELECT i.id, i.repo_id, i.number,
       COALESCE(i.author_id, 0)::BIGINT AS author_id,
       COALESCE(u.username, '')         AS author_name,
       i.agent_role, i.title, i.body, i.state,
       i.branch_name, i.base_branch,
       i.head_sha, i.merge_commit_sha, i.merged_at,
       COALESCE(i.parent_id, 0)::BIGINT AS parent_id, i.parent_number,
       i.created_at, i.updated_at
FROM issues i
LEFT JOIN users u ON u.id = i.author_id
WHERE i.repo_id = sqlc.arg('repo_id') AND i.number = sqlc.arg('number');

-- name: ListIssues :many
-- State arg is optional (NULL = "any state").
SELECT i.id, i.repo_id, i.number,
       COALESCE(i.author_id, 0)::BIGINT AS author_id,
       COALESCE(u.username, '')         AS author_name,
       i.agent_role, i.title, i.body, i.state,
       i.branch_name, i.base_branch,
       i.head_sha, i.merge_commit_sha, i.merged_at,
       COALESCE(i.parent_id, 0)::BIGINT AS parent_id, i.parent_number,
       i.created_at, i.updated_at
FROM issues i
LEFT JOIN users u ON u.id = i.author_id
WHERE i.repo_id = sqlc.arg('repo_id')
  AND (sqlc.narg('state')::TEXT IS NULL OR i.state = sqlc.narg('state'))
ORDER BY i.number DESC
LIMIT sqlc.arg('lim') OFFSET sqlc.arg('off');

-- name: CountIssues :one
SELECT COUNT(*) FROM issues i
WHERE i.repo_id = sqlc.arg('repo_id')
  AND (sqlc.narg('state')::TEXT IS NULL OR i.state = sqlc.narg('state'));

-- name: ListIssueChildren :many
SELECT i.id, i.repo_id, i.number,
       COALESCE(i.author_id, 0)::BIGINT AS author_id,
       COALESCE(u.username, '')         AS author_name,
       i.agent_role, i.title, i.body, i.state,
       i.branch_name, i.base_branch,
       i.head_sha, i.merge_commit_sha, i.merged_at,
       COALESCE(i.parent_id, 0)::BIGINT AS parent_id, i.parent_number,
       i.created_at, i.updated_at
FROM issues i
LEFT JOIN users u ON u.id = i.author_id
WHERE i.parent_id = sqlc.arg('parent_id')
ORDER BY i.number ASC;

-- name: UpdateIssueTitleBody :one
UPDATE issues
SET title = sqlc.arg('title'),
    body = sqlc.arg('body'),
    updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING repo_id, number;

-- name: UpdateIssueState :one
-- Sets state and (when transitioning to merged) merge_commit_sha + merged_at.
UPDATE issues
SET state = sqlc.arg('state'),
    merge_commit_sha = CASE WHEN sqlc.arg('state') = 'merged'
                            THEN sqlc.arg('merge_sha')
                            ELSE merge_commit_sha
                       END,
    merged_at = CASE WHEN sqlc.arg('state') = 'merged' THEN NOW() ELSE merged_at END,
    updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING repo_id, number;

-- name: UpdateIssueHeadSHA :exec
UPDATE issues SET head_sha = sqlc.arg('head_sha'), updated_at = NOW()
WHERE id = sqlc.arg('id');

-- name: ListOpenIssueNumbers :many
SELECT number FROM issues
WHERE repo_id = sqlc.arg('repo_id') AND state = 'open'
ORDER BY number;

-- ---- issue_comments ----

-- name: CreateComment :one
-- author_id and agent_role are mutually exclusive (CHECK constraint at
-- the table level). Callers pass exactly one — sqlc.narg('author_id')
-- for the human path, sqlc.arg('agent_role') for the agent path; the
-- other gets the zero value.
INSERT INTO issue_comments (
    issue_id, author_id, agent_role, body, file_path, line
)
VALUES (
    sqlc.arg('issue_id'),
    sqlc.narg('author_id'),
    sqlc.arg('agent_role'),
    sqlc.arg('body'),
    sqlc.arg('file_path'),
    sqlc.arg('line')
)
RETURNING id, created_at, updated_at;

-- name: GetCommentByID :one
SELECT c.id, c.issue_id,
       COALESCE(c.author_id, 0)::BIGINT AS author_id,
       COALESCE(u.username, '')         AS author_name,
       c.agent_role, c.body, c.file_path, c.line,
       c.created_at, c.updated_at
FROM issue_comments c
LEFT JOIN users u ON u.id = c.author_id
WHERE c.id = sqlc.arg('id');

-- name: ListComments :many
SELECT c.id, c.issue_id,
       COALESCE(c.author_id, 0)::BIGINT AS author_id,
       COALESCE(u.username, '')         AS author_name,
       c.agent_role, c.body, c.file_path, c.line,
       c.created_at, c.updated_at
FROM issue_comments c
LEFT JOIN users u ON u.id = c.author_id
WHERE c.issue_id = sqlc.arg('issue_id')
ORDER BY c.created_at, c.id;

-- ---- issue_events ----

-- name: CreateEvent :one
-- actor_id is nullable for system-generated events (M5+); agent_role
-- is the role-key string for agent-generated events. Both can be set
-- on a row to attribute a system-side action to a specific agent role.
INSERT INTO issue_events (
    issue_id, kind, payload, actor_id, agent_role
)
VALUES (
    sqlc.arg('issue_id'),
    sqlc.arg('kind'),
    sqlc.arg('payload')::jsonb,
    sqlc.narg('actor_id'),
    sqlc.arg('agent_role')
)
RETURNING id, created_at;

-- name: GetEventByID :one
SELECT e.id, e.issue_id, e.kind, e.payload,
       COALESCE(e.actor_id, 0)::BIGINT AS actor_id,
       COALESCE(u.username, '')        AS actor_name,
       e.agent_role, e.created_at
FROM issue_events e
LEFT JOIN users u ON u.id = e.actor_id
WHERE e.id = sqlc.arg('id');

-- name: ListEvents :many
SELECT e.id, e.issue_id, e.kind, e.payload,
       COALESCE(e.actor_id, 0)::BIGINT AS actor_id,
       COALESCE(u.username, '')        AS actor_name,
       e.agent_role, e.created_at
FROM issue_events e
LEFT JOIN users u ON u.id = e.actor_id
WHERE e.issue_id = sqlc.arg('issue_id')
ORDER BY e.created_at, e.id;

-- ---- issue_attachments ----

-- name: CreateAttachment :one
-- Human path: sqlc.narg('author_id'), agent_role=''
-- Agent path: author_id=NULL (omit), agent_role with the role key.
INSERT INTO issue_attachments (
    repo_id, issue_id, author_id, agent_role, storage_key,
    original_name, display_name, size_bytes, mime_type, detected_mime_type,
    sha256, kind, inline, status
)
VALUES (
    sqlc.arg('repo_id'),
    sqlc.arg('issue_id'),
    sqlc.narg('author_id'),
    sqlc.arg('agent_role'),
    sqlc.arg('storage_key'),
    sqlc.arg('original_name'),
    sqlc.arg('display_name'),
    sqlc.arg('size_bytes'),
    sqlc.arg('mime_type'),
    sqlc.arg('detected_mime_type'),
    sqlc.arg('sha256'),
    sqlc.arg('kind'),
    sqlc.arg('inline'),
    sqlc.arg('status')
)
RETURNING id, created_at;

-- name: GetAttachment :one
SELECT a.id, a.repo_id, a.issue_id,
       COALESCE(a.comment_id, 0)::BIGINT AS comment_id,
       COALESCE(a.author_id, 0)::BIGINT   AS author_id,
       a.agent_role, a.storage_key, a.original_name,
       a.display_name, a.size_bytes, a.mime_type, a.detected_mime_type,
       a.sha256, a.kind, a.inline, a.status,
       a.created_at, a.deleted_at
FROM issue_attachments a
WHERE a.id = sqlc.arg('id');

-- name: ListAttachments :many
SELECT a.id, a.repo_id, a.issue_id,
       COALESCE(a.comment_id, 0)::BIGINT AS comment_id,
       COALESCE(a.author_id, 0)::BIGINT   AS author_id,
       a.agent_role, a.storage_key, a.original_name,
       a.display_name, a.size_bytes, a.mime_type, a.detected_mime_type,
       a.sha256, a.kind, a.inline, a.status,
       a.created_at, a.deleted_at
FROM issue_attachments a
WHERE a.issue_id = sqlc.arg('issue_id')
  AND (sqlc.narg('comment_id')::BIGINT IS NULL OR a.comment_id = sqlc.narg('comment_id'))
ORDER BY a.created_at, a.id;

-- name: MarkAttachmentAttached :exec
UPDATE issue_attachments
SET status = 'attached', comment_id = sqlc.arg('comment_id')
WHERE id = sqlc.arg('id') AND status = 'uploaded';

-- name: SoftDeleteAttachment :exec
UPDATE issue_attachments
SET status = 'deleted', deleted_at = NOW()
WHERE id = sqlc.arg('id') AND status <> 'deleted';


-- ---- contributions ----

-- name: UpsertContributionOnPush :one
-- Insert a contribution for a freshly-pushed namespace ref, or update the
-- existing one (keyed by issue_id+ref_name) with the new head/diff. When the
-- head SHA actually changed, the status resets to 'open' (a new push dismisses
-- prior approvals — GitHub-style) and mergeable is recomputed by the caller.
INSERT INTO contributions (
    repo_id, issue_id, session_id, agent_role, ref_name,
    head_sha, base_sha, changed_paths, files, additions, deletions, status
)
VALUES (
    sqlc.arg('repo_id'),
    sqlc.arg('issue_id'),
    sqlc.arg('session_id'),
    sqlc.arg('agent_role'),
    sqlc.arg('ref_name'),
    sqlc.arg('head_sha'),
    sqlc.arg('base_sha'),
    sqlc.arg('changed_paths'),
    sqlc.arg('files'),
    sqlc.arg('additions'),
    sqlc.arg('deletions'),
    'open'
)
ON CONFLICT (issue_id, ref_name) DO UPDATE SET
    session_id    = EXCLUDED.session_id,
    agent_role    = EXCLUDED.agent_role,
    head_sha      = EXCLUDED.head_sha,
    base_sha      = EXCLUDED.base_sha,
    changed_paths = EXCLUDED.changed_paths,
    files         = EXCLUDED.files,
    additions     = EXCLUDED.additions,
    deletions     = EXCLUDED.deletions,
    status        = CASE
                        WHEN contributions.head_sha IS DISTINCT FROM EXCLUDED.head_sha
                             AND contributions.status IN ('open','changes_requested')
                        THEN 'open'
                        ELSE contributions.status
                    END,
    updated_at    = NOW()
RETURNING id;

-- name: GetContribution :one
SELECT id, repo_id, issue_id, session_id, agent_role, ref_name,
       head_sha, base_sha, title, description, status, mergeable,
       merge_mode, changed_paths, files, additions, deletions,
       merged_commit_sha, merged_at, created_at, updated_at
FROM contributions
WHERE id = sqlc.arg('id');

-- name: GetContributionByRef :one
SELECT id, repo_id, issue_id, session_id, agent_role, ref_name,
       head_sha, base_sha, title, description, status, mergeable,
       merge_mode, changed_paths, files, additions, deletions,
       merged_commit_sha, merged_at, created_at, updated_at
FROM contributions
WHERE issue_id = sqlc.arg('issue_id') AND ref_name = sqlc.arg('ref_name');

-- name: ListContributions :many
SELECT id, repo_id, issue_id, session_id, agent_role, ref_name,
       head_sha, base_sha, title, description, status, mergeable,
       merge_mode, changed_paths, files, additions, deletions,
       merged_commit_sha, merged_at, created_at, updated_at
FROM contributions
WHERE issue_id = sqlc.arg('issue_id')
ORDER BY created_at, id;

-- name: SetContributionMeta :one
UPDATE contributions
SET title = sqlc.arg('title'),
    description = sqlc.arg('description'),
    updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING id;

-- name: SetContributionStatus :one
UPDATE contributions
SET status = sqlc.arg('status'), updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING id;

-- name: SetContributionMergeable :exec
UPDATE contributions
SET mergeable = sqlc.arg('mergeable'),
    merge_mode = sqlc.arg('merge_mode'),
    updated_at = NOW()
WHERE id = sqlc.arg('id');

-- name: MarkContributionMerged :one
UPDATE contributions
SET status = 'merged',
    merged_commit_sha = sqlc.arg('merged_commit_sha'),
    merged_at = NOW(),
    mergeable = TRUE,
    updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING id;


