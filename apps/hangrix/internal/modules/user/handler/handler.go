package handler

import (
	"encoding/json"
	"errors"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

type Handler struct {
	users      domain.Repo
	middleware authdomain.Middleware
}

type HandlerDeps struct {
	Users      domain.Repo
	Middleware authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{users: deps.Users, middleware: deps.Middleware}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/users", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/me", h.getMe)
		r.Patch("/me", h.patchMe)
		r.Get("/{id}", h.getByID)
	})
	r.Route("/api/admin/users", func(r chi.Router) {
		r.Use(h.middleware.RequireAdmin)
		r.Get("/", h.adminList)
		r.Patch("/{id}", h.adminUpdate)
		r.Delete("/{id}", h.adminDisable)
	})
}

type publicUser struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	Role      string    `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
}

func toPublic(u *domain.User, includeEmail bool) publicUser {
	p := publicUser{
		ID:        u.ID,
		Username:  u.Username,
		Role:      string(u.Role),
		Disabled:  u.Disabled,
		CreatedAt: u.CreatedAt,
	}
	if includeEmail {
		p.Email = u.Email
	}
	return p
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	u, _ := authdomain.UserFromRequest(r)
	httpx.WriteJSON(w, http.StatusOK, toPublic(u, true))
}

type patchMeReq struct {
	Email       *string `json:"email,omitempty"`
	OldPassword *string `json:"old_password,omitempty"`
	NewPassword *string `json:"new_password,omitempty"`
}

func (h *Handler) patchMe(w http.ResponseWriter, r *http.Request) {
	u, _ := authdomain.UserFromRequest(r)

	var req patchMeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx := r.Context()
	updated := u

	if req.Email != nil {
		email := strings.TrimSpace(strings.ToLower(*req.Email))
		if _, err := mail.ParseAddress(email); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid email")
			return
		}
		out, err := h.users.UpdateProfile(ctx, u.ID, email)
		if err != nil {
			if errors.Is(err, domain.ErrUserConflict) {
				httpx.WriteError(w, http.StatusConflict, "email already in use")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		updated = out
	}

	if req.NewPassword != nil {
		if req.OldPassword == nil {
			httpx.WriteError(w, http.StatusBadRequest, "old_password required")
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(*req.OldPassword)); err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "old password incorrect")
			return
		}
		if len(*req.NewPassword) < 8 {
			httpx.WriteError(w, http.StatusBadRequest, "new password must be >= 8 chars")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "hash failed")
			return
		}
		if err := h.users.UpdatePassword(ctx, u.ID, string(hash)); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	httpx.WriteJSON(w, http.StatusOK, toPublic(updated, true))
}

func (h *Handler) getByID(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	u, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	includeEmail := caller.ID == u.ID || caller.Role == domain.RoleAdmin
	httpx.WriteJSON(w, http.StatusOK, toPublic(u, includeEmail))
}

type listResp struct {
	Items []publicUser `json:"items"`
	Total int64        `json:"total"`
}

func (h *Handler) adminList(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePaging(r)
	ctx := r.Context()

	users, err := h.users.List(ctx, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := h.users.Count(ctx)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]publicUser, 0, len(users))
	for _, u := range users {
		items = append(items, toPublic(u, true))
	}
	httpx.WriteJSON(w, http.StatusOK, listResp{Items: items, Total: total})
}

type adminUpdateReq struct {
	Role     *string `json:"role,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

func (h *Handler) adminUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req adminUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	caller, _ := authdomain.UserFromRequest(r)

	ctx := r.Context()
	var updated *domain.User

	if req.Role != nil {
		role := domain.Role(*req.Role)
		if !role.Valid() {
			httpx.WriteError(w, http.StatusBadRequest, "invalid role")
			return
		}
		if id == caller.ID && role != domain.RoleAdmin {
			httpx.WriteError(w, http.StatusBadRequest, "cannot demote yourself")
			return
		}
		out, err := h.users.UpdateRole(ctx, id, role)
		if err != nil {
			handleUpdateErr(w, err)
			return
		}
		updated = out
	}
	if req.Disabled != nil {
		if id == caller.ID && *req.Disabled {
			httpx.WriteError(w, http.StatusBadRequest, "cannot disable yourself")
			return
		}
		out, err := h.users.UpdateDisabled(ctx, id, *req.Disabled)
		if err != nil {
			handleUpdateErr(w, err)
			return
		}
		updated = out
	}
	if updated == nil {
		httpx.WriteError(w, http.StatusBadRequest, "no changes")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(updated, true))
}

func (h *Handler) adminDisable(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	if id == caller.ID {
		httpx.WriteError(w, http.StatusBadRequest, "cannot disable yourself")
		return
	}
	out, err := h.users.UpdateDisabled(r.Context(), id, true)
	if err != nil {
		handleUpdateErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(out, true))
}

func handleUpdateErr(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrUserNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "user not found")
		return
	}
	httpx.WriteError(w, http.StatusInternalServerError, err.Error())
}

func parsePaging(r *http.Request) (limit, offset int32) {
	limit, offset = 50, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = int32(n)
		}
	}
	return
}
