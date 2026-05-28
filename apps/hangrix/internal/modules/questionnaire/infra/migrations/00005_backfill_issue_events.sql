-- +goose Up
-- Backfill issue_events rows for every existing questionnaire so they
-- appear on the issue timeline. Uses WHERE NOT EXISTS for idempotency —
-- re-running the migration on a DB that already has some events won't
-- create duplicates.
--
-- Runs as questionnaire/00005, after all issue migrations (00001-00016)
-- have completed. By this point issue_events uses the post-00016 schema
-- with actor_id FK (not the old denormalised agent_role/actor_* columns),
-- and the questionnaires + questionnaire_questions tables exist.
--
-- q.created_by_agent is an agent_role_key string (e.g. "maintainer");
-- resolved to actors.id via subquery, with system actor (id=1) as
-- fallback for any agent_role_key not yet seeded in the actors table.
INSERT INTO issue_events (issue_id, kind, payload, actor_id, created_at)
SELECT q.issue_id,
       'questionnaire_posted',
       jsonb_build_object(
           'questionnaire_id', q.id,
           'title',            q.title,
           'question_count',   (SELECT COUNT(*) FROM questionnaire_questions WHERE questionnaire_id = q.id)
       ),
       COALESCE(
           (SELECT id FROM actors WHERE kind = 'agent_role' AND agent_role_key = q.created_by_agent),
           1  -- fallback to system actor (seeded by actor/00001)
       ),
       q.created_at
FROM questionnaires q
WHERE NOT EXISTS (
    SELECT 1 FROM issue_events e
    WHERE e.issue_id = q.issue_id
      AND e.kind = 'questionnaire_posted'
      AND e.payload->>'questionnaire_id' = q.id::text
);

-- +goose Down
DELETE FROM issue_events WHERE kind = 'questionnaire_posted';
