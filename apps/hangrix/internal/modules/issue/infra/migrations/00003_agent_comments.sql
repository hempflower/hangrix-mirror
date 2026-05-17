-- +goose Up
--
-- M7b: let agents author comments and events on issues.
--
-- (1) issue_comments.author_id becomes nullable. Agents have no row in
--     `users` (per principle 3 in ROADMAP — users table is human-only),
--     so an agent comment must be NULL-author. A non-NULL author_id
--     still references users(id); a NULL author_id means "agent" and
--     callers MUST inspect agent_role to know which role wrote it.
--
-- (2) Both tables get an `agent_role` TEXT column. Empty string is the
--     pre-M7b legacy value ("human row"); non-empty is the host yaml
--     role key that authored the row (`backend`, `reviewer`, ...).
--     Validators in service layer enforce the role-key grammar; the
--     column itself stays plain TEXT to keep the migration trivial.
--
-- A CHECK constraint forces exactly one of (human, agent) to be set so
-- the model can't drift into a "neither / both" state — easier to
-- reason about than two independent nullable fields.
ALTER TABLE issue_comments ALTER COLUMN author_id DROP NOT NULL;
ALTER TABLE issue_comments ADD COLUMN agent_role TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_comments
    ADD CONSTRAINT issue_comments_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    );

ALTER TABLE issue_events ADD COLUMN agent_role TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE issue_events DROP COLUMN IF EXISTS agent_role;
ALTER TABLE issue_comments DROP CONSTRAINT IF EXISTS issue_comments_author_xor_agent;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS agent_role;
-- Restore NOT NULL only if no NULL rows survive. We don't enforce in
-- Down — operators rolling back are expected to clean agent rows first.
ALTER TABLE issue_comments ALTER COLUMN author_id SET NOT NULL;
