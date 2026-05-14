-- +goose Up
CREATE TABLE issues (
    id                 BIGSERIAL PRIMARY KEY,
    repo_id            BIGINT      NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    number             BIGINT      NOT NULL,
    author_id          BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    title              TEXT        NOT NULL,
    body               TEXT        NOT NULL DEFAULT '',
    state              TEXT        NOT NULL CHECK (state IN ('open','merged','closed')) DEFAULT 'open',
    branch_name        TEXT        NOT NULL,
    base_branch        TEXT        NOT NULL,
    head_sha           TEXT        NOT NULL DEFAULT '',
    merge_commit_sha   TEXT        NOT NULL DEFAULT '',
    merged_at          TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (repo_id, number)
);

CREATE INDEX idx_issues_repo_state ON issues(repo_id, state);
CREATE INDEX idx_issues_repo_branch ON issues(repo_id, branch_name);

-- Per-repo monotonic issue counter. Bumped on each Issue insert via an
-- explicit UPSERT in the store; cheaper and clearer than a SELECT MAX(number)
-- race or a global sequence.
CREATE TABLE issue_counters (
    repo_id  BIGINT PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
    next     BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE issue_comments (
    id          BIGSERIAL PRIMARY KEY,
    issue_id    BIGINT      NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author_id   BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    body        TEXT        NOT NULL,
    file_path   TEXT        NOT NULL DEFAULT '',
    line        INTEGER     NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_issue_comments_issue ON issue_comments(issue_id, created_at);

CREATE TABLE issue_events (
    id          BIGSERIAL PRIMARY KEY,
    issue_id    BIGINT      NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    kind        TEXT        NOT NULL,
    payload     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    actor_id    BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_issue_events_issue ON issue_events(issue_id, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_issue_events_issue;
DROP TABLE IF EXISTS issue_events;
DROP INDEX IF EXISTS idx_issue_comments_issue;
DROP TABLE IF EXISTS issue_comments;
DROP TABLE IF EXISTS issue_counters;
DROP INDEX IF EXISTS idx_issues_repo_branch;
DROP INDEX IF EXISTS idx_issues_repo_state;
DROP TABLE IF EXISTS issues;
