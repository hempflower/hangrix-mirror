-- +goose Up

-- M7a introduces a repo kind discriminator so list / search endpoints can
-- filter on `kind=agent`. Detection is content-driven: a repo is an "agent"
-- repo iff the tip of its default branch contains a root `agent.yml`. The
-- column caches the detection so we don't have to spawn a `git cat-file`
-- for every list request — the receive-pack post-receive flow updates it
-- on each push to the default branch.
--
-- Default 'standard' (the existing kind) so the migration is a pure
-- backfill — no behaviour change for non-agent repos. M7a's push-side
-- detector flips the column on first push that includes a valid
-- agent.yml; agents_config schema validation runs there too (rejecting
-- container / env / secrets / volumes fields per principle 7).
ALTER TABLE repos
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'standard'
    CHECK (kind IN ('standard', 'agent'));

-- Partial index because the agent set is small and "list agent repos" is
-- the only filter we expect to see at scale; non-agent listings stay on
-- the existing owner indexes.
CREATE INDEX repos_kind_agent_idx ON repos (kind) WHERE kind = 'agent';

-- +goose Down
DROP INDEX IF EXISTS repos_kind_agent_idx;
ALTER TABLE repos DROP COLUMN kind;
