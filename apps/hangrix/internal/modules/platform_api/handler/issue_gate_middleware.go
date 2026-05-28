package handler

import (
	"errors"
	"net/http"

	issuegatedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue_gate/domain"
)

// IssueGateMiddleware is a chi-compatible middleware that gates every
// /api/v1 call on the session's issue state. It runs after BearerAuth
// (which stores the session in the request context) and returns a
// standard 403 JSON envelope when the issue is closed or merged.
//
// Human browser sessions (no session token in context) pass through
// untouched — the gate only fires for authenticated agent sessions.
func IssueGateMiddleware(gate issuegatedomain.IssueActivityGate) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := GetSession(r)
			if sess == nil || sess.IssueNumber == nil || sess.RepoID == nil {
				// No agent session — likely a human browser request
				// or a session that hasn't been bound to an issue yet.
				next.ServeHTTP(w, r)
				return
			}

			err := gate.CheckIssue(r.Context(), *sess.RepoID, *sess.IssueNumber)
			if err != nil {
				var term *issuegatedomain.ErrIssueTerminal
				if errors.As(err, &term) {
					WriteJSON(w, http.StatusForbidden, map[string]any{
						"error":        term.Error(),
						"code":         "issue_terminal",
						"issue_number": term.IssueNumber,
						"issue_state":  string(term.State),
					})
					return
				}
				// Non-terminal errors (DB failures) are internal
				// errors — don't leak details to the agent.
				WriteError(w, http.StatusInternalServerError, "internal error checking issue state")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
