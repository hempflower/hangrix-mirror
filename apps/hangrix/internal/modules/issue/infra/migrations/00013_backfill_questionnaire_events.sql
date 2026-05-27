-- +goose Up
-- Backfill issue_events rows for every existing questionnaire so they
-- appear on the issue timeline. Uses WHERE NOT EXISTS for idempotency —
-- re-running the migration on a DB that already has some events won't
-- create duplicates.
INSERT INTO issue_events (issue_id, kind, payload, agent_role, created_at,
                           actor_kind, actor_role_key, actor_display_name)
SELECT q.issue_id,
       'questionnaire_posted',
       jsonb_build_object(
           'questionnaire_id', q.id,
           'title',            q.title,
           'question_count',   (SELECT COUNT(*) FROM questionnaire_questions WHERE questionnaire_id = q.id)
       ),
       q.created_by_agent,
       q.created_at,
       'agent',
       q.created_by_agent,
       q.created_by_agent
FROM questionnaires q
WHERE NOT EXISTS (
    SELECT 1 FROM issue_events e
    WHERE e.issue_id = q.issue_id
      AND e.kind = 'questionnaire_posted'
      AND e.payload->>'questionnaire_id' = q.id::text
);

-- +goose Down
DELETE FROM issue_events WHERE kind = 'questionnaire_posted';
