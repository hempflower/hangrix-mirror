-- +goose Up

-- M7c retires the agent-as-repo design: there's no longer a kind=agent
-- classification, so the `kind` column + its partial index become dead
-- weight. The post-push detector and KindRefresher are deleted in the
-- same change.
DROP INDEX IF EXISTS repos_kind_agent_idx;
ALTER TABLE repos DROP COLUMN IF EXISTS kind;

-- +goose Down
ALTER TABLE repos
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'standard'
    CHECK (kind IN ('standard', 'agent'));
CREATE INDEX repos_kind_agent_idx ON repos (kind) WHERE kind = 'agent';
