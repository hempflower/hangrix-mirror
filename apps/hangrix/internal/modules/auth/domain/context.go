package domain

import (
	"context"
	"net/http"

	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

type ctxKey struct{}

var userCtxKey = ctxKey{}

func WithUser(ctx context.Context, u *userdomain.User) context.Context {
	return context.WithValue(ctx, userCtxKey, u)
}

func UserFrom(ctx context.Context) (*userdomain.User, bool) {
	u, ok := ctx.Value(userCtxKey).(*userdomain.User)
	return u, ok
}

func UserFromRequest(r *http.Request) (*userdomain.User, bool) {
	return UserFrom(r.Context())
}

// Middleware exposes the auth gates used by other modules' handlers. It is
// implemented in the auth package proper; consumers depend on this interface
// via the ioc container so the handler-layer never imports auth's internals.
type Middleware interface {
	RequireAuth(next http.Handler) http.Handler
	RequireAdmin(next http.Handler) http.Handler
}
