// Package handler exposes the platform's agent API over HTTP — the
// GitHub-style REST surface under /api/v1/.
//
// Shared auth middleware (bearerAuth / actorFromRequest) lives in
// auth.go; response helpers (WriteJSON, WriteError, etc.) in respond.go.
package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

type ctxKey int

const (
	ctxKeySession   ctxKey = iota // *runnerdomain.AgentSession (legacy)
	ctxKeyActor               // *apidomain.Actor (v1)
)

// GetSession returns the AgentSession stored by the legacy bearerAuth
// middleware. Returns nil when the middleware hasn't run.
func GetSession(r *http.Request) *runnerdomain.AgentSession {
	sess, _ := r.Context().Value(ctxKeySession).(*runnerdomain.AgentSession)
	return sess
}

// GetActor returns the Actor stored by the v1 auth middleware.
// Returns nil when the middleware hasn't run.
func GetActor(r *http.Request) *apidomain.Actor {
	p, _ := r.Context().Value(ctxKeyActor).(*apidomain.Actor)
	return p
}

// BearerAuth is a chi-compatible middleware that resolves
// Authorization: Bearer hgxs_... → AgentSession and stores the Actor
// (for v1 handlers) in the request context. It also validates the hgxr_
// runner token for the attachment download endpoint.
//
// 401 on missing/malformed header; 403 on token invalid/inactive.
func BearerAuth(validator runnerdomain.SessionTokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(raw, prefix) {
				WriteError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
			if token == "" {
				WriteError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			sess, err := validator.ValidateSessionToken(r.Context(), token)
			if err != nil {
				switch {
				case errors.Is(err, runnerdomain.ErrInvalidSessionToken):
					WriteError(w, http.StatusForbidden, "invalid session token")
				case errors.Is(err, runnerdomain.ErrSessionTokenInactive):
					WriteError(w, http.StatusForbidden, "session token revoked or session terminated")
				default:
					WriteError(w, http.StatusInternalServerError, err.Error())
				}
				return
			}
			actor := apidomain.NewActor(sess)
			ctx := context.WithValue(r.Context(), ctxKeySession, sess)
			ctx = context.WithValue(ctx, ctxKeyActor, actor)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
