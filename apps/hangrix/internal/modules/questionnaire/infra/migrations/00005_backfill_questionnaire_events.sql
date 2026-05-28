-- +goose Up
-- Backfill issue_events rows for every existing questionnaire so they
-- appear on the issue timeline. Moved here from the issue module's 00013
-- (which has been deleted) because it references questionnaire tables.
--
-- Uses actor_id (current schema after issue/00016 replaced denormalized
-- actor_* columns) by looking up the agent actor via the actors table.
-- Falls back to the system actor (id=1) when the agent actor row hasn't
-- been seeded yet. WHERE NOT EXISTS keeps the migration idempotent.
INSERT INTO issue_events (issue_id, kind, payload, actor_id, created_at)
SELECT q.issue_id,
       'questionnaire_posted',
       jsonb_build_object(
           'questionnaire_id', q.id,
           'title',            q.title,
           'question_count',   (SELECT COUNT(*)
                                FROM questionnaire_questions
                                WHERE questionnaire_id = q.id)
       ),
       COALESCE(
           (SELECT a.id
            FROM actors a
            WHERE a.kind = 'agent_role'
              AND a.agent_role_key = q.created_by_agent
            LIMIT 1),
           1  -- system actor fallback
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
