-- +goose Up
-- Contribution-branch model (replaces the text-patch model). Each row is a
-- git branch in an agent's per-issue namespace (refs/heads/issue-<N>/<role>)
-- that behaves as an independent merge-request: reviews + votes attach to the
-- branch (not the issue), and the server merges approved branches into the
-- issue branch. See docs/contribution-branches.md.
--
-- The legacy issue_patches / issue_patch_files tables are retired here; the
-- patch-first flow and its agent_api tools are gone.
CREATE TABLE contributions (
    id                BIGSERIAL PRIMARY KEY,
    repo_id           BIGINT      NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    issue_id          BIGINT      NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    session_id        BIGINT      NOT NULL DEFAULT 0,
    agent_role        TEXT        NOT NULL,            -- role key = namespace owner
    ref_name          TEXT        NOT NULL,            -- refs/heads/issue-<N>/<role>[/slug]
    head_sha          TEXT        NOT NULL DEFAULT '', -- current contribution head
    base_sha          TEXT        NOT NULL DEFAULT '', -- issue head this was diffed against
    title             TEXT        NOT NULL DEFAULT '',
    description       TEXT        NOT NULL DEFAULT '',
    -- Status is derived from the branch's required reviewers + votes and
    -- cached here (see ComputeContributionReviewStatus). Branches are
    -- immutable once pushed, so 'pending' is the create-time default.
    status            TEXT        NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending','approved','rejected','merged','closed')),
    mergeable         BOOLEAN     NOT NULL DEFAULT TRUE,
    merge_mode        TEXT        NOT NULL DEFAULT '',  -- last CheckAutoMerge mode
    changed_paths     TEXT[]      NOT NULL DEFAULT '{}',
    files             INT         NOT NULL DEFAULT 0,
    additions         INT         NOT NULL DEFAULT 0,
    deletions         INT         NOT NULL DEFAULT 0,
    merged_commit_sha TEXT        NOT NULL DEFAULT '',  -- server-computed on apply
    merged_at         TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (issue_id, ref_name)
);

CREATE INDEX idx_contributions_issue ON contributions(issue_id);
CREATE INDEX idx_contributions_repo ON contributions(repo_id);
CREATE INDEX idx_contributions_status ON contributions(status);

DROP TABLE IF EXISTS issue_patch_files;
DROP TABLE IF EXISTS issue_patches;

-- +goose Down
DROP TABLE IF EXISTS contributions;
