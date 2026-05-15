package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

type Handler struct {
	cookieName   string
	cookieSecure bool
	sessionTTL   time.Duration

	sessions   domain.SessionStore
	users      userdomain.Repo
	orgs       orgdomain.OrgRepo
	middleware domain.Middleware
}

type HandlerDeps struct {
	Config   *config.Config
	Sessions domain.SessionStore
	Users    userdomain.Repo
	// Orgs is used at registration time to keep the user-name namespace
	// disjoint from the org-name namespace. The two failure modes
	// ("org already taken" / "user already taken") collapse into one
	// 409 — the caller picks a different name either way.
	Orgs       orgdomain.OrgRepo
	Middleware domain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		cookieName:   deps.Config.Auth.CookieName,
		cookieSecure: deps.Config.Auth.CookieSecure,
		sessionTTL:   deps.Config.Auth.SessionTTL,
		sessions:     deps.Sessions,
		users:        deps.Users,
		orgs:         deps.Orgs,
		middleware:   deps.Middleware,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", h.register)
		r.Post("/login", h.login)
		r.Post("/logout", h.logout)
		r.With(h.middleware.RequireAuth).Get("/me", h.me)
	})
}

type registerReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResp struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
}

func toUserResp(u *userdomain.User) userResp {
	return userResp{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		Role:      string(u.Role),
		Disabled:  u.Disabled,
		CreatedAt: u.CreatedAt,
	}
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	if len(req.Username) < 3 || len(req.Username) > 32 {
		writeError(w, http.StatusBadRequest, "username must be 3-32 chars")
		return
	}
	// Reserved-name + cross-namespace (orgs) check. We refuse to mint a user
	// whose username would shadow an existing org name; the org route
	// /{owner}/{name} treats both kinds uniformly, so a collision would
	// silently make one of them unreachable.
	if orgdomain.IsReservedName(req.Username) {
		writeError(w, http.StatusConflict, "username is reserved")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		writeError(w, http.StatusBadRequest, "invalid email")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be >= 8 chars")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash failed")
		return
	}

	ctx := r.Context()
	if exists, err := h.orgs.Exists(ctx, req.Username); err != nil {
		writeError(w, http.StatusInternalServerError, "name lookup failed")
		return
	} else if exists {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}
	count, err := h.users.Count(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count users failed")
		return
	}
	role := userdomain.RoleUser
	if count == 0 {
		role = userdomain.RoleAdmin
	}

	u, err := h.users.Create(ctx, req.Username, req.Email, string(hash), role)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserConflict) {
			writeError(w, http.StatusConflict, "username or email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.issueSession(w, r, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toUserResp(u))
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)

	ctx := r.Context()
	u, err := h.users.GetByUsername(ctx, req.Username)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u.Disabled {
		writeError(w, http.StatusForbidden, "account disabled")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := h.issueSession(w, r, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toUserResp(u))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.cookieName); err == nil {
		_ = h.sessions.Delete(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, ok := domain.UserFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, toUserResp(u))
}

func (h *Handler) issueSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(h.sessionTTL)
	if _, err := h.sessions.Create(r.Context(), token, userID, expiresAt); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// keep context import in case future flows need it
var _ = context.Background
