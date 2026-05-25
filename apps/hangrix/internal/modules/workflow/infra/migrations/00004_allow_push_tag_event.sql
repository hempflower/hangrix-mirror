-- +goose Up
-- The original workflow_runs CHECK constraint (00001) predates the
-- repo.push_tag event and never listed it, so every tag-triggered run
-- insert failed with a 23514 check violation — silently, inside
-- createTag's detached goroutine. Replace the constraint with one that
-- includes repo.push_tag. Fix-forward: editing 00001 in place would not
-- re-run on databases already migrated past it.
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_event_name_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_event_name_check
    CHECK (event_name IN ('repo.push', 'repo.push_tag', 'issue.opened', 'issue.comment', 'workflow.dispatch'));

-- +goose Down
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_event_name_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_event_name_check
    CHECK (event_name IN ('repo.push', 'issue.opened', 'issue.comment', 'workflow.dispatch'));
