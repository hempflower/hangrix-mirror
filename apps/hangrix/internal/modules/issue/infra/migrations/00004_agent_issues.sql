-- +goose Up
--
-- M7b (continued from 00003): let agents be issue authors.
--
-- (1) issues.author_id becomes nullable. Agents have no row in `users`,
--     so an agent-created issue must be NULL-author. A non-NULL author_id
--     still references users(id); a NULL author_id means "agent" and
--     callers MUST inspect agent_role to know which role created it.
--
-- (2) issues gets an `agent_role` TEXT column. Empty string is the
--     pre-M7b legacy value ("human row"); non-empty is the host yaml
--     role key that created the issue. Same semantics as the
--     issue_comments.agent_role column from 00003.
--
-- A CHECK constraint forces exactly one of (human, agent) to be set.
ALTER TABLE issues ALTER COLUMN author_id DROP NOT NULL;
ALTER TABLE issues ADD COLUMN agent_role TEXT NOT NULL DEFAULT '';
ALTER TABLE issues
    ADD CONSTRAINT issues_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    );

-- +goose Down
ALTER TABLE issues DROP CONSTRAINT IF EXISTS issues_author_xor_agent;
ALTER TABLE issues DROP COLUMN IF EXISTS agent_role;
-- Restore NOT NULL only if no NULL rows survive. Operators rolling back
-- are expected to clean agent-created issues first.
ALTER TABLE issues ALTER COLUMN author_id SET NOT NULL;
