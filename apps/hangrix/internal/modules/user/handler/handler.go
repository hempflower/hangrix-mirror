package handler

import (
	"encoding/json"
	"errors"
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
	writeJSON(w, http.StatusOK, toPublic(u, true))
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
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx := r.Context()
	updated := u

	if req.Email != nil {
		email := strings.TrimSpace(strings.ToLower(*req.Email))
		if _, err := mail.ParseAddress(email); err != nil {
			writeError(w, http.StatusBadRequest, "invalid email")
			return
		}
		out, err := h.users.UpdateProfile(ctx, u.ID, email)
		if err != nil {
			if errors.Is(err, domain.ErrUserConflict) {
				writeError(w, http.StatusConflict, "email already in use")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		updated = out
	}

	if req.NewPassword != nil {
		if req.OldPassword == nil {
			writeError(w, http.StatusBadRequest, "old_password required")
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(*req.OldPassword)); err != nil {
			writeError(w, http.StatusUnauthorized, "old password incorrect")
			return
		}
		if len(*req.NewPassword) < 8 {
			writeError(w, http.StatusBadRequest, "new password must be >= 8 chars")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "hash failed")
			return
		}
		if err := h.users.UpdatePassword(ctx, u.ID, string(hash)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, toPublic(updated, true))
}

func (h *Handler) getByID(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	u, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	includeEmail := caller.ID == u.ID || caller.Role == domain.RoleAdmin
	writeJSON(w, http.StatusOK, toPublic(u, includeEmail))
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := h.users.Count(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]publicUser, 0, len(users))
	for _, u := range users {
		items = append(items, toPublic(u, true))
	}
	writeJSON(w, http.StatusOK, listResp{Items: items, Total: total})
}

type adminUpdateReq struct {
	Role     *string `json:"role,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

func (h *Handler) adminUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req adminUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	caller, _ := authdomain.UserFromRequest(r)

	ctx := r.Context()
	var updated *domain.User

	if req.Role != nil {
		role := domain.Role(*req.Role)
		if !role.Valid() {
			writeError(w, http.StatusBadRequest, "invalid role")
			return
		}
		if id == caller.ID && role != domain.RoleAdmin {
			writeError(w, http.StatusBadRequest, "cannot demote yourself")
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
			writeError(w, http.StatusBadRequest, "cannot disable yourself")
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
		writeError(w, http.StatusBadRequest, "no changes")
		return
	}
	writeJSON(w, http.StatusOK, toPublic(updated, true))
}

func (h *Handler) adminDisable(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	if id == caller.ID {
		writeError(w, http.StatusBadRequest, "cannot disable yourself")
		return
	}
	out, err := h.users.UpdateDisabled(r.Context(), id, true)
	if err != nil {
		handleUpdateErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPublic(out, true))
}

func handleUpdateErr(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func parseID(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
