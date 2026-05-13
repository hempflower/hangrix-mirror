package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// Middleware is the concrete implementation of domain.Middleware. It looks up
// the cookie session, loads the user, gates by role, and injects the user into
// the request context via domain.WithUser.
type Middleware struct {
	cookieName string
	sessions   domain.SessionStore
	users      userdomain.Repo
}

type MiddlewareDeps struct {
	Config   *config.Config
	Sessions domain.SessionStore
	Users    userdomain.Repo
}

func NewMiddleware(deps *MiddlewareDeps) *Middleware {
	return &Middleware{
		cookieName: deps.Config.Auth.CookieName,
		sessions:   deps.Sessions,
		users:      deps.Users,
	}
}

func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := m.resolveUser(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if u.Disabled {
			http.Error(w, "account disabled", http.StatusForbidden)
			return
		}
		ctx := domain.WithUser(r.Context(), u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Middleware) RequireAdmin(next http.Handler) http.Handler {
	return m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := domain.UserFromRequest(r)
		if !ok || u.Role != userdomain.RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (m *Middleware) resolveUser(r *http.Request) (*userdomain.User, error) {
	c, err := r.Cookie(m.cookieName)
	if err != nil {
		return nil, errors.New("no session cookie")
	}
	ctx := r.Context()
	s, err := m.sessions.Get(ctx, c.Value)
	if err != nil {
		return nil, err
	}
	if time.Now().After(s.ExpiresAt) {
		_ = m.sessions.Delete(context.Background(), s.Token)
		return nil, errors.New("session expired")
	}
	u, err := m.users.GetByID(ctx, s.UserID)
	if err != nil {
		return nil, err
	}
	return u, nil
}
