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
