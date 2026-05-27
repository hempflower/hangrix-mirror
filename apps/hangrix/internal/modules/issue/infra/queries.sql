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
-- actor_* columns are dual-written alongside legacy fields.
INSERT INTO issues (
    repo_id, number, author_id, agent_role, title, body, branch_name,
    base_branch, parent_id, parent_number,
    actor_kind, actor_user_id, actor_role_key, actor_workflow_run_id, actor_display_name
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
    sqlc.arg('parent_number'),
    sqlc.arg('actor_kind'),
    sqlc.narg('actor_user_id'),
    sqlc.arg('actor_role_key'),
    sqlc.narg('actor_workflow_run_id'),
    sqlc.arg('actor_display_name')
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
       i.created_at, i.updated_at,
       i.actor_kind,
       COALESCE(i.actor_user_id, 0)::BIGINT AS actor_user_id,
       i.actor_role_key,
       COALESCE(i.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       i.actor_display_name
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
       i.created_at, i.updated_at,
       i.actor_kind,
       COALESCE(i.actor_user_id, 0)::BIGINT AS actor_user_id,
       i.actor_role_key,
       COALESCE(i.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       i.actor_display_name
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
       i.created_at, i.updated_at,
       i.actor_kind,
       COALESCE(i.actor_user_id, 0)::BIGINT AS actor_user_id,
       i.actor_role_key,
       COALESCE(i.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       i.actor_display_name
FROM issues i
LEFT JOIN users u ON u.id = i.author_id
WHERE i.parent_id = sqlc.arg('parent_id')
ORDER BY i.number ASC;

-- name: ListOpenDescendantIssues :many
-- Recursive walk over issues.parent_id starting at $1. Emits open rows.
-- Depth=1 for direct children, >1 deeper. Visited-set guard defends
-- against accidental cycles in case a future write violates the tree.
WITH RECURSIVE descendants AS (
    SELECT i.id, i.number, i.title, i.state, 1 AS depth,
           ARRAY[i.id] AS visited
    FROM issues i
    WHERE i.parent_id = sqlc.arg('root_id')
  UNION ALL
    SELECT i.id, i.number, i.title, i.state, d.depth + 1,
           d.visited || i.id
    FROM issues i
    JOIN descendants d ON i.parent_id = d.id
    WHERE i.id <> ALL(d.visited)
      AND d.depth < 32                     -- hard ceiling
)
SELECT id, number, title, state, depth::INT AS depth
FROM descendants
WHERE state = 'open'
ORDER BY depth ASC, number ASC;

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
-- actor_* columns are dual-written alongside legacy fields.
INSERT INTO issue_comments (
    issue_id, author_id, agent_role, body, file_path, line,
    actor_kind, actor_user_id, actor_role_key, actor_workflow_run_id, actor_display_name
)
VALUES (
    sqlc.arg('issue_id'),
    sqlc.narg('author_id'),
    sqlc.arg('agent_role'),
    sqlc.arg('body'),
    sqlc.arg('file_path'),
    sqlc.arg('line'),
    sqlc.arg('actor_kind'),
    sqlc.narg('actor_user_id'),
    sqlc.arg('actor_role_key'),
    sqlc.narg('actor_workflow_run_id'),
    sqlc.arg('actor_display_name')
)
RETURNING id, created_at, updated_at;

-- name: GetCommentByID :one
SELECT c.id, c.issue_id,
       COALESCE(c.author_id, 0)::BIGINT AS author_id,
       COALESCE(u.username, '')         AS author_name,
       c.agent_role, c.body, c.file_path, c.line,
       c.created_at, c.updated_at,
       c.actor_kind,
       COALESCE(c.actor_user_id, 0)::BIGINT AS actor_user_id,
       c.actor_role_key,
       COALESCE(c.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       c.actor_display_name
FROM issue_comments c
LEFT JOIN users u ON u.id = c.author_id
WHERE c.id = sqlc.arg('id');

-- name: ListComments :many
SELECT c.id, c.issue_id,
       COALESCE(c.author_id, 0)::BIGINT AS author_id,
       COALESCE(u.username, '')         AS author_name,
       c.agent_role, c.body, c.file_path, c.line,
       c.created_at, c.updated_at,
       c.actor_kind,
       COALESCE(c.actor_user_id, 0)::BIGINT AS actor_user_id,
       c.actor_role_key,
       COALESCE(c.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       c.actor_display_name
FROM issue_comments c
LEFT JOIN users u ON u.id = c.author_id
WHERE c.issue_id = sqlc.arg('issue_id')
ORDER BY c.created_at, c.id;

-- ---- issue_events ----

-- name: CreateEvent :one
-- actor_id is nullable for system-generated events (M5+); agent_role
-- is the role-key string for agent-generated events. Both can be set
-- on a row to attribute a system-side action to a specific agent role.
-- actor_* columns are dual-written alongside legacy fields.
INSERT INTO issue_events (
    issue_id, kind, payload, actor_id, agent_role,
    actor_kind, actor_user_id, actor_role_key, actor_workflow_run_id, actor_display_name
)
VALUES (
    sqlc.arg('issue_id'),
    sqlc.arg('kind'),
    sqlc.arg('payload')::jsonb,
    sqlc.narg('actor_id'),
    sqlc.arg('agent_role'),
    sqlc.arg('actor_kind'),
    sqlc.narg('actor_user_id'),
    sqlc.arg('actor_role_key'),
    sqlc.narg('actor_workflow_run_id'),
    sqlc.arg('actor_display_name')
)
RETURNING id, created_at;

-- name: GetEventByID :one
SELECT e.id, e.issue_id, e.kind, e.payload,
       COALESCE(e.actor_id, 0)::BIGINT AS actor_id,
       COALESCE(u.username, '')        AS actor_name,
       e.agent_role, e.created_at,
       e.actor_kind,
       COALESCE(e.actor_user_id, 0)::BIGINT AS actor_user_id,
       e.actor_role_key,
       COALESCE(e.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       e.actor_display_name
FROM issue_events e
LEFT JOIN users u ON u.id = e.actor_id
WHERE e.id = sqlc.arg('id');

-- name: ListEvents :many
SELECT e.id, e.issue_id, e.kind, e.payload,
       COALESCE(e.actor_id, 0)::BIGINT AS actor_id,
       COALESCE(u.username, '')        AS actor_name,
       e.agent_role, e.created_at,
       e.actor_kind,
       COALESCE(e.actor_user_id, 0)::BIGINT AS actor_user_id,
       e.actor_role_key,
       COALESCE(e.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       e.actor_display_name
FROM issue_events e
LEFT JOIN users u ON u.id = e.actor_id
WHERE e.issue_id = sqlc.arg('issue_id')
ORDER BY e.created_at, e.id;

-- ---- issue_attachments ----

-- name: CreateAttachment :one
-- Human path: sqlc.narg('author_id'), agent_role=''
-- Agent path: author_id=NULL (omit), agent_role with the role key.
-- actor_* columns are dual-written alongside legacy fields.
INSERT INTO issue_attachments (
    repo_id, issue_id, author_id, agent_role, storage_key,
    original_name, display_name, size_bytes, mime_type, detected_mime_type,
    sha256, kind, inline, status,
    actor_kind, actor_user_id, actor_role_key, actor_workflow_run_id, actor_display_name
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
    sqlc.arg('status'),
    sqlc.arg('actor_kind'),
    sqlc.narg('actor_user_id'),
    sqlc.arg('actor_role_key'),
    sqlc.narg('actor_workflow_run_id'),
    sqlc.arg('actor_display_name')
)
RETURNING id, created_at;

-- name: GetAttachment :one
SELECT a.id, a.repo_id, a.issue_id,
       COALESCE(a.comment_id, 0)::BIGINT AS comment_id,
       COALESCE(a.author_id, 0)::BIGINT   AS author_id,
       a.agent_role, a.storage_key, a.original_name,
       a.display_name, a.size_bytes, a.mime_type, a.detected_mime_type,
       a.sha256, a.kind, a.inline, a.status,
       a.created_at, a.deleted_at,
       a.actor_kind,
       COALESCE(a.actor_user_id, 0)::BIGINT AS actor_user_id,
       a.actor_role_key,
       COALESCE(a.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       a.actor_display_name
FROM issue_attachments a
WHERE a.id = sqlc.arg('id');

-- name: ListAttachments :many
SELECT a.id, a.repo_id, a.issue_id,
       COALESCE(a.comment_id, 0)::BIGINT AS comment_id,
       COALESCE(a.author_id, 0)::BIGINT   AS author_id,
       a.agent_role, a.storage_key, a.original_name,
       a.display_name, a.size_bytes, a.mime_type, a.detected_mime_type,
       a.sha256, a.kind, a.inline, a.status,
       a.created_at, a.deleted_at,
       a.actor_kind,
       COALESCE(a.actor_user_id, 0)::BIGINT AS actor_user_id,
       a.actor_role_key,
       COALESCE(a.actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       a.actor_display_name
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
-- Insert a contribution for a freshly-pushed namespace ref. New rows start in
-- 'pending'; the caller recomputes the real status from the branch's required
-- reviewers afterwards. Contribution branches are immutable once pushed (the
-- git layer rejects re-pushes to an existing ref), so the ON CONFLICT path
-- only fires on idempotent re-delivery of the same push — it refreshes the
-- diff snapshot but leaves the review status untouched.
-- actor_* columns are dual-written alongside legacy agent_role.
INSERT INTO contributions (
    repo_id, issue_id, session_id, agent_role, ref_name,
    head_sha, base_sha, changed_paths, files, additions, deletions, status,
    actor_kind, actor_role_key, actor_display_name
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
    'pending',
    sqlc.arg('actor_kind'),
    sqlc.arg('actor_role_key'),
    sqlc.arg('actor_display_name')
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
    actor_kind    = EXCLUDED.actor_kind,
    actor_role_key = EXCLUDED.actor_role_key,
    actor_display_name = EXCLUDED.actor_display_name,
    updated_at    = NOW()
RETURNING id;

-- name: GetContribution :one
SELECT id, repo_id, issue_id, session_id, agent_role, ref_name,
       head_sha, base_sha, title, description, status, mergeable,
       merge_mode, changed_paths, files, additions, deletions,
       merged_commit_sha, merged_at, created_at, updated_at,
       actor_kind,
       COALESCE(actor_user_id, 0)::BIGINT AS actor_user_id,
       actor_role_key,
       COALESCE(actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       actor_display_name
FROM contributions
WHERE id = sqlc.arg('id');

-- name: GetContributionByRef :one
SELECT id, repo_id, issue_id, session_id, agent_role, ref_name,
       head_sha, base_sha, title, description, status, mergeable,
       merge_mode, changed_paths, files, additions, deletions,
       merged_commit_sha, merged_at, created_at, updated_at,
       actor_kind,
       COALESCE(actor_user_id, 0)::BIGINT AS actor_user_id,
       actor_role_key,
       COALESCE(actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       actor_display_name
FROM contributions
WHERE issue_id = sqlc.arg('issue_id') AND ref_name = sqlc.arg('ref_name');

-- name: ListContributions :many
-- include_closed / include_merged are optional booleans that control
-- whether terminal (closed / merged) contributions appear in the result.
-- When both are false (the default), only non-terminal contributions
-- (pending, approved, rejected) are returned — the "active" view.
SELECT id, repo_id, issue_id, session_id, agent_role, ref_name,
       head_sha, base_sha, title, description, status, mergeable,
       merge_mode, changed_paths, files, additions, deletions,
       merged_commit_sha, merged_at, created_at, updated_at,
       actor_kind,
       COALESCE(actor_user_id, 0)::BIGINT AS actor_user_id,
       actor_role_key,
       COALESCE(actor_workflow_run_id, 0)::BIGINT AS actor_workflow_run_id,
       actor_display_name
FROM contributions
WHERE issue_id = sqlc.arg('issue_id')
  AND (sqlc.arg('include_closed')::BOOLEAN OR status <> 'closed')
  AND (sqlc.arg('include_merged')::BOOLEAN OR status <> 'merged')
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


-- ---- todos ----

-- name: ListTodos :many
SELECT id, issue_id, content, status, position, created_at, updated_at
FROM todos
WHERE issue_id = sqlc.arg('issue_id')
ORDER BY position, id;

-- name: CreateTodo :one
INSERT INTO todos (issue_id, content, status, position)
VALUES (
    sqlc.arg('issue_id'),
    sqlc.arg('content'),
    sqlc.arg('status'),
    sqlc.arg('position')
)
RETURNING id, issue_id, content, status, position, created_at, updated_at;

-- name: GetTodoByID :one
SELECT id, issue_id, content, status, position, created_at, updated_at
FROM todos
WHERE id = sqlc.arg('id');


-- name: UpdateTodoStatus :one
UPDATE todos
SET status = sqlc.arg('status'),
    content = COALESCE(sqlc.narg('content'), content),
    updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING id, issue_id, content, status, position, created_at, updated_at;

-- name: UpdateTodoContent :one
UPDATE todos
SET content = sqlc.arg('content'),
    updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING id, issue_id, content, status, position, created_at, updated_at;

-- name: DeleteTodo :exec
DELETE FROM todos WHERE id = sqlc.arg('id');

-- name: CountTodosByStatus :many
SELECT status, COUNT(*)::BIGINT AS count
FROM todos
WHERE issue_id = sqlc.arg('issue_id')
GROUP BY status;


-- ---- issue_dependencies ----

-- name: AddDependency :one
INSERT INTO issue_dependencies (repo_id, issue_id, depends_on_id, created_by)
VALUES (sqlc.arg('repo_id'), sqlc.arg('issue_id'), sqlc.arg('depends_on_id'), sqlc.narg('created_by'))
ON CONFLICT (issue_id, depends_on_id) DO NOTHING
RETURNING id, repo_id, issue_id, depends_on_id,
          COALESCE(created_by, 0)::BIGINT AS created_by,
          created_at;

-- name: RemoveDependency :exec
DELETE FROM issue_dependencies
WHERE issue_id = sqlc.arg('issue_id') AND depends_on_id = sqlc.arg('depends_on_id');

-- name: ListDependenciesFor :many
-- Returns edges where issue_id matches (what this issue depends on).
SELECT id, repo_id, issue_id, depends_on_id,
       COALESCE(created_by, 0)::BIGINT AS created_by,
       created_at
FROM issue_dependencies
WHERE issue_id = sqlc.arg('issue_id')
ORDER BY created_at;

-- name: ListDependenciesBlocking :many
-- Returns edges where depends_on_id matches (what this issue blocks).
SELECT id, repo_id, issue_id, depends_on_id,
       COALESCE(created_by, 0)::BIGINT AS created_by,
       created_at
FROM issue_dependencies
WHERE depends_on_id = sqlc.arg('issue_id')
ORDER BY created_at;

-- ReachableForward is handled via raw pgxpool query in infra.go
-- (the recursive CTE confuses sqlc's parser).

-- name: ListDepsForSubtree :many
-- Returns every dependency edge where both issue_id and depends_on_id
-- belong to the subtree rooted at root_id.
WITH RECURSIVE subtree AS (
    SELECT i.id AS node_id FROM issues i WHERE i.id = sqlc.arg('root_id')
  UNION
    SELECT i.id FROM issues i JOIN subtree s ON i.parent_id = s.node_id
)
SELECT d.id, d.repo_id, d.issue_id, d.depends_on_id,
       COALESCE(d.created_by, 0)::BIGINT AS created_by,
       d.created_at
FROM issue_dependencies d
WHERE d.issue_id IN (SELECT node_id FROM subtree);

-- ---- plan subtree (recursive CTE for the whole tree) ----

-- name: PlanSubtree :many
-- Returns every issue in the subtree rooted at root_id, including the root,
-- with depth for tree rendering. Ordered depth-first by number.
WITH RECURSIVE plan_tree AS (
    SELECT i.id, i.number, i.title, i.state, i.agent_role,
           i.parent_id,
           i.actor_kind,
           COALESCE(i.actor_user_id, 0)::BIGINT AS actor_user_id,
           i.actor_role_key,
           i.actor_display_name,
           0::INT AS depth,
           ARRAY[i.number] AS path
    FROM issues i
    WHERE i.id = sqlc.arg('root_id')
  UNION ALL
    SELECT i.id, i.number, i.title, i.state, i.agent_role,
           i.parent_id,
           i.actor_kind,
           COALESCE(i.actor_user_id, 0)::BIGINT AS actor_user_id,
           i.actor_role_key,
           i.actor_display_name,
           pt.depth + 1,
           pt.path || i.number
    FROM issues i
    JOIN plan_tree pt ON i.parent_id = pt.id
    WHERE pt.depth < 32
)
SELECT id, number, title, state, agent_role,
       COALESCE(parent_id, 0)::BIGINT AS parent_id,
       actor_kind, actor_user_id, actor_role_key, actor_display_name,
       depth::INT
FROM plan_tree
ORDER BY path;
