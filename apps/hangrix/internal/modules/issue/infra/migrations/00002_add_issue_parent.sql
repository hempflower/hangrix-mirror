-- +goose Up
-- Sub-issue link. parent_id is nullable because a top-level issue has no
-- parent; ON DELETE SET NULL keeps children alive when a parent is removed
-- (rare — parents shouldn't be hard-deleted, but if a migration ever does,
-- losing the parent is a tolerable graceful-degradation outcome).
ALTER TABLE issues ADD COLUMN parent_id     BIGINT REFERENCES issues(id) ON DELETE SET NULL;
-- parent_number is the denormalized number of the parent for cheap list
-- views ("show me all children of #42"). Kept in sync inside the same
-- transaction that sets parent_id.
ALTER TABLE issues ADD COLUMN parent_number BIGINT NOT NULL DEFAULT 0;

CREATE INDEX idx_issues_parent ON issues(repo_id, parent_id);

-- +goose Down
DROP INDEX IF EXISTS idx_issues_parent;
ALTER TABLE issues DROP COLUMN IF EXISTS parent_number;
ALTER TABLE issues DROP COLUMN IF EXISTS parent_id;
