-- Questionnaire queries: CRUD + answers + results.
-- sqlc.arg / sqlc.narg used for named parameters.

-- name: CreateQuestionnaire :one
INSERT INTO questionnaires (issue_id, title, description, status, created_by_agent)
VALUES (sqlc.arg('issue_id'), sqlc.arg('title'), sqlc.arg('description'), 'open', sqlc.arg('created_by_agent'))
RETURNING id, issue_id, title, description, status, created_by_agent, created_at, closed_at, closed_reason;

-- name: CreateQuestion :one
INSERT INTO questionnaire_questions (questionnaire_id, position, question_text, qtype, options, required)
VALUES (sqlc.arg('questionnaire_id'), sqlc.arg('position'), sqlc.arg('question_text'), sqlc.arg('qtype'), sqlc.arg('options'), sqlc.arg('required'))
RETURNING id, questionnaire_id, position, question_text, qtype, options, required;

-- name: GetQuestionnaire :one
SELECT id, issue_id, title, description, status, created_by_agent, created_at, closed_at, closed_reason
FROM questionnaires
WHERE id = sqlc.arg('id');

-- name: GetQuestions :many
SELECT id, questionnaire_id, position, question_text, qtype, options, required
FROM questionnaire_questions
WHERE questionnaire_id = sqlc.arg('questionnaire_id')
ORDER BY position ASC;

-- name: GetQuestionnairesByIssue :many
SELECT id, issue_id, title, description, status, created_by_agent, created_at, closed_at, closed_reason
FROM questionnaires
WHERE issue_id = sqlc.arg('issue_id')
ORDER BY created_at DESC;

-- name: CloseQuestionnaire :one
UPDATE questionnaires
SET status = 'closed', closed_at = now(), closed_reason = sqlc.arg('closed_reason')
WHERE id = sqlc.arg('id')
RETURNING id, issue_id, title, description, status, created_by_agent, created_at, closed_at, closed_reason;

-- name: InsertAnswer :one
INSERT INTO questionnaire_answers (questionnaire_id, actor_id, answers)
VALUES (sqlc.arg('questionnaire_id'), sqlc.arg('actor_id'), sqlc.arg('answers'))
RETURNING id, questionnaire_id, actor_id, answers, submitted_at, updated_at;

-- name: AutoCloseQuestionnaire :one
UPDATE questionnaires
SET status = 'closed', closed_at = now(),
    closed_reason = COALESCE(NULLIF(sqlc.arg('closed_reason'), ''), 'auto:first_submission')
WHERE id = sqlc.arg('id') AND status = 'open'
RETURNING id, issue_id, title, description, status, created_by_agent, created_at, closed_at, closed_reason;

-- name: GetUserAnswer :one
SELECT id, questionnaire_id, actor_id, answers, submitted_at, updated_at
FROM questionnaire_answers
WHERE questionnaire_id = sqlc.arg('questionnaire_id') AND actor_id = sqlc.arg('actor_id');

-- name: ListAnswers :many
SELECT id, questionnaire_id, actor_id, answers, submitted_at, updated_at
FROM questionnaire_answers
WHERE questionnaire_id = sqlc.arg('questionnaire_id')
ORDER BY submitted_at ASC;

-- name: CountAnswers :one
SELECT COUNT(*)::BIGINT AS cnt
FROM questionnaire_answers
WHERE questionnaire_id = sqlc.arg('questionnaire_id');

-- name: GetStatusForUpdate :one
SELECT status
FROM questionnaires
WHERE id = sqlc.arg('id')
FOR UPDATE;
