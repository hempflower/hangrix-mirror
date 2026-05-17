-- +goose Up

-- M7c retires the agent-as-repo design: there's no longer a separate
-- agent repo to pin, so the agent_repo / agent_sha columns become dead
-- weight. The audit snapshot collapses to a single repo_sha (the host
-- repo's tip), the spawner stops resolving `<owner>/<name>@<ref>`, and
-- the runner bundle endpoint + cache go away in the same change.
ALTER TABLE agent_sessions
    DROP COLUMN IF EXISTS agent_repo,
    DROP COLUMN IF EXISTS agent_sha;

-- +goose Down

ALTER TABLE agent_sessions
    ADD COLUMN agent_repo TEXT NOT NULL DEFAULT '',
    ADD COLUMN agent_sha  TEXT NOT NULL DEFAULT '';
