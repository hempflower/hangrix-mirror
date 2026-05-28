// Package domain declares the questionnaire module's types, interfaces,
// and pure-data validation. No I/O, no crypto, no regex on persisted state.
package domain

// AnsweredEventPayload is the wake-up payload emitted to the spawner when
// a user submits an answer. It carries the answer metadata plus the full
// result (or an error reason when result building fails) so the agent can
// act on the submission without an extra check_questionnaire round-trip.
type AnsweredEventPayload struct {
	QuestionnaireID int64   `json:"questionnaire_id"`
	AnswerID        int64   `json:"answer_id"`
	RespondentID    int64   `json:"respondent_id"`
	Result          *Result `json:"result,omitempty"`
	ResultError     string  `json:"result_error,omitempty"`
}
