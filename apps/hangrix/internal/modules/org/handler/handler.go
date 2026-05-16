// Package handler exposes the org module's HTTP surface: org CRUD, member
// management, and a couple of read-only helpers used by the web UI to
// power "switch owner" pickers. Authorization is enforced inline:
//
//   - Read endpoints are open to any authenticated user — orgs do not carry
//     a private/public flag.
//   - Mutating endpoints (PATCH / DELETE / member writes) require the caller
//     to be an `owner`-role member, or a platform admin.
//
// All routes deliberately sit under /api/orgs/{name} rather than reusing the
// /{owner}/... namespace — the latter belongs to the web frontend and routes
// to repo / profile views.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// orgNameRe matches the same character class as usernames so a name can
// validly belong to either users.username or organizations.name.
var orgNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

type Handler struct {
	orgs       domain.OrgRepo
	users      userdomain.Repo
	middleware authdomain.Middleware
}

type HandlerDeps struct {
	Orgs       domain.OrgRepo
	Users      userdomain.Repo
	Middleware authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{orgs: deps.Orgs, users: deps.Users, middleware: deps.Middleware}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/orgs", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Post("/", h.create)
		r.Get("/", h.listForCaller)
		r.Get("/{name}", h.getOne)
		r.Patch("/{name}", h.patchOne)
		r.Delete("/{name}", h.deleteOne)

		r.Get("/{name}/members", h.listMembers)
		r.Post("/{name}/members", h.addMember)
		r.Patch("/{name}/members/{username}", h.patchMember)
		r.Delete("/{name}/members/{username}", h.removeMember)
	})

	r.Route("/api/users/{username}/orgs", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.listForUser)
	})
}

// ---- DTOs ----

type publicOrg struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	AvatarURL   string    `json:"avatar_url"`
	CreatedBy   int64     `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toPublic(o *domain.Org) publicOrg {
	return publicOrg{
		ID:          o.ID,
		Name:        o.Name,
		DisplayName: o.DisplayName,
		Description: o.Description,
		AvatarURL:   o.AvatarURL,
		CreatedBy:   o.CreatedBy,
		CreatedAt:   o.CreatedAt,
		UpdatedAt:   o.UpdatedAt,
	}
}

type publicMember struct {
	UserID   int64     `json:"user_id"`
	Username string    `json:"username"`
	Role     string    `json:"role"`
	AddedAt  time.Time `json:"added_at"`
	AddedBy  int64     `json:"added_by"`
}

func toPublicMember(m *domain.Membership) publicMember {
	return publicMember{
		UserID:   m.UserID,
		Username: m.Username,
		Role:     string(m.Role),
		AddedAt:  m.AddedAt,
		AddedBy:  m.AddedBy,
	}
}

// ---- create / read ----

type createReq struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Description = strings.TrimSpace(req.Description)
	if !orgNameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if domain.IsReservedName(req.Name) {
		httpx.WriteError(w, http.StatusConflict, "name is reserved")
		return
	}

	ctx := r.Context()
	// Reject names colliding with an existing user. Org-name uniqueness
	// inside organizations is enforced by the DB; cross-table uniqueness
	// (users ∪ orgs) we enforce here.
	if _, err := h.users.GetByUsername(ctx, req.Name); err == nil {
		httpx.WriteError(w, http.StatusConflict, "name already taken")
		return
	} else if !errors.Is(err, userdomain.ErrUserNotFound) {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	org, err := h.orgs.Create(ctx, req.Name, req.DisplayName, req.Description, caller.ID)
	if err != nil {
		if errors.Is(err, domain.ErrOrgConflict) {
			httpx.WriteError(w, http.StatusConflict, "name already taken")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Creator becomes the bootstrap owner. If membership write fails the
	// org row is orphaned with zero members — we surface a 500; admin can
	// recover the row out-of-band.
	if err := h.orgs.AddMember(ctx, org.ID, caller.ID, caller.ID, domain.RoleOwner); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "seed owner: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublic(org))
}

func (h *Handler) getOne(w http.ResponseWriter, r *http.Request) {
	org, ok := h.loadOrgByName(w, r)
	if !ok {
		return
	}
	if !h.canRead(r, org) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(org))
}

type patchReq struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

func (h *Handler) patchOne(w http.ResponseWriter, r *http.Request) {
	org, ok := h.loadOrgByName(w, r)
	if !ok {
		return
	}
	if !h.canManage(r, org) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req patchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	displayName := org.DisplayName
	if req.DisplayName != nil {
		displayName = strings.TrimSpace(*req.DisplayName)
	}
	description := org.Description
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	avatarURL := org.AvatarURL
	if req.AvatarURL != nil {
		avatarURL = strings.TrimSpace(*req.AvatarURL)
	}
	updated, err := h.orgs.UpdateMeta(r.Context(), org.ID, displayName, description, avatarURL)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(updated))
}

func (h *Handler) deleteOne(w http.ResponseWriter, r *http.Request) {
	org, ok := h.loadOrgByName(w, r)
	if !ok {
		return
	}
	if !h.canManage(r, org) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := h.orgs.SoftDelete(r.Context(), org.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listForCaller serves `GET /api/orgs?member_of=me` — the "orgs I belong to"
// drop-down source for the new-repo form.
func (h *Handler) listForCaller(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	memberOf := r.URL.Query().Get("member_of")
	if memberOf != "me" {
		httpx.WriteError(w, http.StatusBadRequest, "only member_of=me is supported")
		return
	}
	orgs, err := h.orgs.ListOrgsForUser(r.Context(), caller.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicOrg, 0, len(orgs))
	for _, o := range orgs {
		items = append(items, toPublic(o))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// listForUser serves `GET /api/users/{username}/orgs` — used on the user
// profile page. Any authenticated user can see the full list; org rows do
// not carry a visibility flag.
func (h *Handler) listForUser(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	if !orgNameRe.MatchString(username) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid username")
		return
	}
	target, err := h.users.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	orgs, err := h.orgs.ListOrgsForUser(r.Context(), target.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicOrg, 0, len(orgs))
	for _, o := range orgs {
		items = append(items, toPublic(o))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ---- members ----

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	org, ok := h.loadOrgByName(w, r)
	if !ok {
		return
	}
	if !h.canRead(r, org) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	members, err := h.orgs.ListMembers(r.Context(), org.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]publicMember, 0, len(members))
	for _, m := range members {
		out = append(out, toPublicMember(m))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

type addMemberReq struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (h *Handler) addMember(w http.ResponseWriter, r *http.Request) {
	org, ok := h.loadOrgByName(w, r)
	if !ok {
		return
	}
	if !h.canManage(r, org) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req addMemberReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !orgNameRe.MatchString(req.Username) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid username")
		return
	}
	role := domain.Role(strings.TrimSpace(req.Role))
	if role == "" {
		role = domain.RoleMember
	}
	if !role.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid role")
		return
	}

	target, err := h.users.GetByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	if err := h.orgs.AddMember(r.Context(), org.ID, target.ID, caller.ID, role); err != nil {
		if errors.Is(err, domain.ErrMemberConflict) {
			httpx.WriteError(w, http.StatusConflict, "already a member")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	m, err := h.orgs.GetMember(r.Context(), org.ID, target.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublicMember(m))
}

type patchMemberReq struct {
	Role string `json:"role"`
}

func (h *Handler) patchMember(w http.ResponseWriter, r *http.Request) {
	org, ok := h.loadOrgByName(w, r)
	if !ok {
		return
	}
	if !h.canManage(r, org) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	target, ok := h.loadMemberUser(w, r)
	if !ok {
		return
	}

	var req patchMemberReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	role := domain.Role(strings.TrimSpace(req.Role))
	if !role.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid role")
		return
	}

	// Block demoting the last owner. We pull the current member first so a
	// no-op (role unchanged) doesn't churn the row's audit fields and so
	// non-existent members get a clean 404.
	current, err := h.orgs.GetMember(r.Context(), org.ID, target.ID)
	if err != nil {
		if errors.Is(err, domain.ErrMemberNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "member not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if current.Role == domain.RoleOwner && role != domain.RoleOwner {
		owners, err := h.orgs.CountOwners(r.Context(), org.ID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if owners <= 1 {
			httpx.WriteError(w, http.StatusConflict, "cannot demote the last owner")
			return
		}
	}

	if err := h.orgs.UpdateMemberRole(r.Context(), org.ID, target.ID, role); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	m, err := h.orgs.GetMember(r.Context(), org.ID, target.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicMember(m))
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	org, ok := h.loadOrgByName(w, r)
	if !ok {
		return
	}
	target, ok := h.loadMemberUser(w, r)
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	// A user may always remove themselves; otherwise it's an owner-only
	// operation.
	if caller.ID != target.ID && !h.canManage(r, org) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	current, err := h.orgs.GetMember(r.Context(), org.ID, target.ID)
	if err != nil {
		if errors.Is(err, domain.ErrMemberNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "member not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if current.Role == domain.RoleOwner {
		owners, err := h.orgs.CountOwners(r.Context(), org.ID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if owners <= 1 {
			httpx.WriteError(w, http.StatusConflict, "cannot remove the last owner")
			return
		}
	}
	if err := h.orgs.RemoveMember(r.Context(), org.ID, target.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ----

func (h *Handler) loadOrgByName(w http.ResponseWriter, r *http.Request) (*domain.Org, bool) {
	name := chi.URLParam(r, "name")
	if !orgNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return nil, false
	}
	org, err := h.orgs.GetByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrOrgNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "org not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return org, true
}

func (h *Handler) loadMemberUser(w http.ResponseWriter, r *http.Request) (*userdomain.User, bool) {
	username := chi.URLParam(r, "username")
	if !orgNameRe.MatchString(username) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid username")
		return nil, false
	}
	u, err := h.users.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return u, true
}

// canRead returns true for any authenticated caller. Org rows do not carry
// a visibility flag; every signed-in user can browse the profile and
// member list. The unused org argument is kept so the call sites stay
// symmetric with canManage.
func (h *Handler) canRead(r *http.Request, _ *domain.Org) bool {
	caller, _ := authdomain.UserFromRequest(r)
	return caller != nil
}

func (h *Handler) canManage(r *http.Request, org *domain.Org) bool {
	caller, _ := authdomain.UserFromRequest(r)
	if caller == nil {
		return false
	}
	if caller.Role == userdomain.RoleAdmin {
		return true
	}
	role, ok, err := h.membership(r.Context(), org.ID, caller.ID)
	if err != nil || !ok {
		return false
	}
	return role == domain.RoleOwner
}

// membership translates a missing member row into (Role(""), false, nil)
// so the predicate-style callers above can flatten the three-way "yes / no /
// error" result without sprinkling error checks at every call site.
func (h *Handler) membership(ctx context.Context, orgID, userID int64) (domain.Role, bool, error) {
	m, err := h.orgs.GetMember(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrMemberNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return m.Role, true, nil
}
