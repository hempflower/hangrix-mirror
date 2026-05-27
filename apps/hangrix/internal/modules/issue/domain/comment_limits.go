package domain

import (
	"fmt"
	"unicode/utf8"
)

// MaxCommentBodyRunes is the application-level cap on issue_comments.body,
// measured in Unicode code points (runes). The Postgres column is plain
// TEXT — this constant is the only place the limit is enforced.
//
// When adjusting this value, also update the agent-side schema hint in
// apps/hangrix-agent/internal/tools/platform/platform.go (stringPropMax
// for the issue_comment tool).
const MaxCommentBodyRunes = 8000

// ErrCommentBodyTooLong is returned by ValidateCommentBody when the
// supplied body exceeds MaxCommentBodyRunes. Carrying the actual rune
// count on the error lets the handler include it in the structured
// response without recomputing.
type ErrCommentBodyTooLong struct {
	Runes int // actual rune count of the rejected body
	Limit int // == MaxCommentBodyRunes, snapshotted for the error
}

func (e *ErrCommentBodyTooLong) Error() string {
	return fmt.Sprintf("comment body too long: %d runes (limit %d)", e.Runes, e.Limit)
}

// ValidateCommentBody returns *ErrCommentBodyTooLong when body exceeds
// MaxCommentBodyRunes. It does NOT check for empty body — that policy
// lives in the handler (it differs between v1 and legacy endpoints).
func ValidateCommentBody(body string) error {
	runes := utf8.RuneCountInString(body)
	if runes > MaxCommentBodyRunes {
		return &ErrCommentBodyTooLong{Runes: runes, Limit: MaxCommentBodyRunes}
	}
	return nil
}
