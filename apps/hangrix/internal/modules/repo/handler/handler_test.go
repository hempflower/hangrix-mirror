package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	tokendomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// ------- stubs -------

type stubStore struct {
	byOwnerAndName func(ownerKind domain.OwnerKind, ownerID int64, name string) (*domain.Repo, error)
}

func (s *stubStore) GetByOwnerAndName(_ context.Context, ownerKind domain.OwnerKind, ownerID int64, name string) (*domain.Repo, error) {
	return s.byOwnerAndName(ownerKind, ownerID, name)
}
func (s *stubStore) Create(_ context.Context, _ domain.OwnerKind, _ int64, _, _, _ string, _ domain.Visibility) (*domain.Repo, error) {
	return nil, nil
}
func (s *stubStore) GetByID(_ context.Context, _ int64) (*domain.Repo, error) { return nil, nil }
func (s *stubStore) ListByOwner(_ context.Context, _ domain.OwnerKind, _ int64, _ bool, _, _ int32) ([]*domain.Repo, int64, error) {
	return nil, 0, nil
}
func (s *stubStore) Delete(_ context.Context, _ int64) error { return nil }
func (s *stubStore) UpdateMeta(_ context.Context, _ int64, _, _ string, _ domain.Visibility) (*domain.Repo, error) {
	return nil, nil
}
func (s *stubStore) Transfer(_ context.Context, _ int64, _ domain.OwnerKind, _ int64) (*domain.Repo, error) {
	return nil, nil
}

type stubMemberStore struct {
	add    func(repoID, userID, addedBy int64, role domain.MemberRole) error
	update func(repoID, userID int64, role domain.MemberRole) error
	remove func(repoID, userID int64) error
	list   func(repoID int64) ([]*domain.RepoMember, error)
	get    func(repoID, userID int64) (*domain.RepoMember, error)
}

func (s *stubMemberStore) AddMember(_ context.Context, repoID, userID, addedBy int64, role domain.MemberRole) error {
	return s.add(repoID, userID, addedBy, role)
}
func (s *stubMemberStore) UpdateMemberRole(_ context.Context, repoID, userID int64, role domain.MemberRole) error {
	return s.update(repoID, userID, role)
}
func (s *stubMemberStore) RemoveMember(_ context.Context, repoID, userID int64) error {
	return s.remove(repoID, userID)
}
func (s *stubMemberStore) ListMembers(_ context.Context, repoID int64) ([]*domain.RepoMember, error) {
	return s.list(repoID)
}
func (s *stubMemberStore) GetMember(_ context.Context, repoID, userID int64) (*domain.RepoMember, error) {
	return s.get(repoID, userID)
}

type stubUserRepo struct {
	byUsername func(username string) (*userdomain.User, error)
}

func (s *stubUserRepo) GetByUsername(_ context.Context, username string) (*userdomain.User, error) {
	return s.byUsername(username)
}
func (s *stubUserRepo) Create(_ context.Context, _, _, _ string, _ userdomain.Role) (*userdomain.User, error) {
	return nil, nil
}
func (s *stubUserRepo) GetByID(_ context.Context, _ int64) (*userdomain.User, error) { return nil, nil }
func (s *stubUserRepo) GetByEmail(_ context.Context, _ string) (*userdomain.User, error) {
	return nil, nil
}
func (s *stubUserRepo) Count(_ context.Context) (int64, error) { return 0, nil }
func (s *stubUserRepo) List(_ context.Context, _, _ int32) ([]*userdomain.User, error) {
	return nil, nil
}
func (s *stubUserRepo) UpdateProfile(_ context.Context, _ int64, _ string) (*userdomain.User, error) {
	return nil, nil
}
func (s *stubUserRepo) UpdatePassword(_ context.Context, _ int64, _ string) error { return nil }
func (s *stubUserRepo) UpdateRole(_ context.Context, _ int64, _ userdomain.Role) (*userdomain.User, error) {
	return nil, nil
}
func (s *stubUserRepo) UpdateDisabled(_ context.Context, _ int64, _ bool) (*userdomain.User, error) {
	return nil, nil
}

type stubResolver struct {
	resolveOwner func(name string) (*orgdomain.Owner, error)
	membership   func(orgID, userID int64) (orgdomain.Role, bool, error)
}

func (s *stubResolver) ResolveOwner(_ context.Context, name string) (*orgdomain.Owner, error) {
	return s.resolveOwner(name)
}
func (s *stubResolver) Membership(_ context.Context, orgID, userID int64) (orgdomain.Role, bool, error) {
	return s.membership(orgID, userID)
}

// authMiddleware injects a pre-canned user into every request.
type authMiddleware struct {
	user *userdomain.User
}

func (m authMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := authdomain.WithUser(r.Context(), m.user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (m authMiddleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := authdomain.WithUser(r.Context(), m.user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ------- helpers -------

var (
	testOwner = &userdomain.User{ID: 1, Username: "testuser", Role: userdomain.RoleUser}
	testOther = &userdomain.User{ID: 2, Username: "otheruser", Role: userdomain.RoleUser}
	testRepo  = &domain.Repo{
		ID:         10,
		OwnerKind:  domain.OwnerKindUser,
		OwnerID:    1,
		OwnerName:  "testuser",
		Name:       "testrepo",
		Visibility: domain.VisibilityPublic,
	}
	testOrgRepo = &domain.Repo{
		ID:         20,
		OwnerKind:  domain.OwnerKindOrg,
		OwnerID:    5,
		OwnerName:  "testorg",
		Name:       "orgrepo",
		Visibility: domain.VisibilityPublic,
	}
)

func newTestHandler(caller *userdomain.User, store *stubStore, members *stubMemberStore, users *stubUserRepo, resolver *stubResolver) *Handler {
	if store == nil {
		store = &stubStore{
			byOwnerAndName: func(ownerKind domain.OwnerKind, ownerID int64, name string) (*domain.Repo, error) {
				if ownerKind == domain.OwnerKindUser && ownerID == 1 && name == "testrepo" {
					return testRepo, nil
				}
				if ownerKind == domain.OwnerKindOrg && ownerID == 5 && name == "orgrepo" {
					return testOrgRepo, nil
				}
				return nil, domain.ErrRepoNotFound
			},
		}
	}
	return &Handler{
		store:      store,
		members:    members,
		users:      users,
		resolver:   resolver,
		middleware: authMiddleware{user: caller},
	}
}

func newRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func jsonBody(v any) *bytes.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// ------- add member tests -------

func TestAddMember_Success(t *testing.T) {
	members := &stubMemberStore{
		add: func(repoID, userID, addedBy int64, role domain.MemberRole) error { return nil },
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{
				RepoID: repoID, UserID: userID, Username: "otheruser",
				Role: domain.MemberRoleWrite, AddedBy: 1,
			}, nil
		},
	}
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) {
			if username == "otheruser" {
				return testOther, nil
			}
			return nil, userdomain.ErrUserNotFound
		},
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, members, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := jsonBody(map[string]string{"username": "otheruser", "role": "write"})
	resp, err := http.Post(srv.URL+"/api/repos/testuser/testrepo/members", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
}

func TestAddMember_DefaultRoleRead(t *testing.T) {
	var capturedRole domain.MemberRole
	members := &stubMemberStore{
		add: func(repoID, userID, addedBy int64, role domain.MemberRole) error {
			capturedRole = role
			return nil
		},
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{RepoID: repoID, UserID: userID, Username: "otheruser", Role: capturedRole, AddedBy: 1}, nil
		},
	}
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) {
			return testOther, nil
		},
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, members, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	// Send empty role → should default to "read".
	body := jsonBody(map[string]string{"username": "otheruser"})
	resp, err := http.Post(srv.URL+"/api/repos/testuser/testrepo/members", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if capturedRole != domain.MemberRoleRead {
		t.Fatalf("captured role = %q, want %q", capturedRole, domain.MemberRoleRead)
	}
}

func TestAddMember_OrgRepo_Rejected(t *testing.T) {
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindOrg, ID: 5, Name: name}, nil
		},
		membership: func(orgID, userID int64) (orgdomain.Role, bool, error) {
			return orgdomain.RoleOwner, true, nil
		},
	}
	h := newTestHandler(testOwner, nil, &stubMemberStore{}, &stubUserRepo{}, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := jsonBody(map[string]string{"username": "otheruser", "role": "read"})
	resp, err := http.Post(srv.URL+"/api/repos/testorg/orgrepo/members", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAddMember_AlreadyMember(t *testing.T) {
	members := &stubMemberStore{
		add: func(repoID, userID, addedBy int64, role domain.MemberRole) error {
			return domain.ErrRepoMemberConflict
		},
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{RepoID: repoID, UserID: userID, Username: "otheruser", Role: domain.MemberRoleRead, AddedBy: 1}, nil
		},
	}
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) { return testOther, nil },
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, members, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := jsonBody(map[string]string{"username": "otheruser", "role": "read"})
	resp, err := http.Post(srv.URL+"/api/repos/testuser/testrepo/members", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestAddMember_InvalidRole(t *testing.T) {
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) { return testOther, nil },
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, &stubMemberStore{}, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := jsonBody(map[string]string{"username": "otheruser", "role": "admin"})
	resp, err := http.Post(srv.URL+"/api/repos/testuser/testrepo/members", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAddMember_OwnerAsMember(t *testing.T) {
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) {
			if username == "testuser" {
				return testOwner, nil
			}
			return nil, userdomain.ErrUserNotFound
		},
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, &stubMemberStore{}, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := jsonBody(map[string]string{"username": "testuser", "role": "read"})
	resp, err := http.Post(srv.URL+"/api/repos/testuser/testrepo/members", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// ------- list members tests -------

func TestListMembers_UserOwned(t *testing.T) {
	members := &stubMemberStore{
		list: func(repoID int64) ([]*domain.RepoMember, error) {
			return []*domain.RepoMember{
				{RepoID: repoID, UserID: 2, Username: "otheruser", Role: domain.MemberRoleWrite, AddedBy: 1},
			}, nil
		},
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, members, &stubUserRepo{}, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/repos/testuser/testrepo/members")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Items []publicRepoMember `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(body.Items))
	}
	if body.Items[0].Username != "otheruser" {
		t.Fatalf("username = %q, want otheruser", body.Items[0].Username)
	}
	if body.Items[0].Role != "write" {
		t.Fatalf("role = %q, want write", body.Items[0].Role)
	}
}

func TestListMembers_OrgRepo_Rejected(t *testing.T) {
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindOrg, ID: 5, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, &stubMemberStore{}, &stubUserRepo{}, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/repos/testorg/orgrepo/members")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// ------- patch member tests -------

func TestPatchMember_Success(t *testing.T) {
	var updatedRole domain.MemberRole
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{RepoID: repoID, UserID: userID, Username: "otheruser", Role: domain.MemberRoleRead, AddedBy: 1}, nil
		},
		update: func(repoID, userID int64, role domain.MemberRole) error {
			updatedRole = role
			return nil
		},
	}
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) { return testOther, nil },
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, members, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := strings.NewReader(`{"role":"write"}`)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/repos/testuser/testrepo/members/otheruser", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if updatedRole != domain.MemberRoleWrite {
		t.Fatalf("updated role = %q, want write", updatedRole)
	}
}

func TestPatchMember_NoOpWhenSameRole(t *testing.T) {
	updateCalled := false
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{RepoID: repoID, UserID: userID, Username: "otheruser", Role: domain.MemberRoleRead, AddedBy: 1}, nil
		},
		update: func(repoID, userID int64, role domain.MemberRole) error {
			updateCalled = true
			return nil
		},
	}
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) { return testOther, nil },
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, members, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := strings.NewReader(`{"role":"read"}`)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/repos/testuser/testrepo/members/otheruser", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if updateCalled {
		t.Fatal("UpdateMemberRole was called but should have been skipped (no-op)")
	}
}

func TestPatchMember_NotFound(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return nil, domain.ErrRepoMemberNotFound
		},
	}
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) { return testOther, nil },
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, members, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	body := strings.NewReader(`{"role":"write"}`)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/repos/testuser/testrepo/members/otheruser", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// ------- remove member tests -------

func TestRemoveMember_SelfRemove(t *testing.T) {
	// otheruser removes themselves from the repo.  They are NOT the owner
	// but should be allowed to self-remove.
	removeCalled := false
	members := &stubMemberStore{
		remove: func(repoID, userID int64) error {
			removeCalled = true
			return nil
		},
	}
	users := &stubUserRepo{
		byUsername: func(username string) (*userdomain.User, error) {
			if username == "otheruser" {
				return testOther, nil
			}
			return nil, userdomain.ErrUserNotFound
		},
	}
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: 1, Name: name}, nil
		},
	}
	// Caller is testOther (not the owner).
	h := newTestHandler(testOther, nil, members, users, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/repos/testuser/testrepo/members/otheruser", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if !removeCalled {
		t.Fatal("RemoveMember was not called")
	}
}

func TestRemoveMember_OrgRepo_Rejected(t *testing.T) {
	resolver := &stubResolver{
		resolveOwner: func(name string) (*orgdomain.Owner, error) {
			return &orgdomain.Owner{Kind: orgdomain.OwnerKindOrg, ID: 5, Name: name}, nil
		},
	}
	h := newTestHandler(testOwner, nil, &stubMemberStore{}, &stubUserRepo{}, resolver)
	srv := httptest.NewServer(newRouter(h))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/repos/testorg/orgrepo/members/otheruser", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// ------- permission helpers -------

func TestViewerPermission_Owner(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return nil, domain.ErrRepoMemberNotFound
		},
	}
	h := newTestHandler(testOwner, nil, members, &stubUserRepo{}, &stubResolver{})
	perm := h.viewerPermission(context.Background(), testOwner, testRepo)
	if perm != "manage" {
		t.Fatalf("viewerPermission = %q, want manage", perm)
	}
}

func TestViewerPermission_WriteMember(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{Role: domain.MemberRoleWrite}, nil
		},
	}
	h := newTestHandler(testOther, nil, members, &stubUserRepo{}, &stubResolver{})
	perm := h.viewerPermission(context.Background(), testOther, testRepo)
	if perm != "write" {
		t.Fatalf("viewerPermission = %q, want write", perm)
	}
}

func TestViewerPermission_ReadMember(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{Role: domain.MemberRoleRead}, nil
		},
	}
	h := newTestHandler(testOther, nil, members, &stubUserRepo{}, &stubResolver{})
	perm := h.viewerPermission(context.Background(), testOther, testRepo)
	if perm != "read" {
		t.Fatalf("viewerPermission = %q, want read", perm)
	}
}

func TestViewerPermission_None(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return nil, domain.ErrRepoMemberNotFound
		},
	}
	h := newTestHandler(testOther, nil, members, &stubUserRepo{}, &stubResolver{})
	perm := h.viewerPermission(context.Background(), testOther, testRepo)
	if perm != "" {
		t.Fatalf("viewerPermission = %q, want empty", perm)
	}
}

func TestCanReadRepo_Member(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{Role: domain.MemberRoleRead}, nil
		},
	}
	h := newTestHandler(testOther, nil, members, &stubUserRepo{}, &stubResolver{})
	can, err := h.canReadRepo(context.Background(), testOther, testRepo)
	if err != nil {
		t.Fatalf("canReadRepo: %v", err)
	}
	if !can {
		t.Fatal("expected canReadRepo = true for read member")
	}
}

func TestCanReadRepo_NonMember(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return nil, domain.ErrRepoMemberNotFound
		},
	}
	h := newTestHandler(testOther, nil, members, &stubUserRepo{}, &stubResolver{})
	can, err := h.canReadRepo(context.Background(), testOther, testRepo)
	if err != nil {
		t.Fatalf("canReadRepo: %v", err)
	}
	if can {
		t.Fatal("expected canReadRepo = false for non-member")
	}
}

func TestCanWriteContents_WriteMember(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{Role: domain.MemberRoleWrite}, nil
		},
	}
	h := newTestHandler(testOther, nil, members, &stubUserRepo{}, &stubResolver{})
	can, err := h.canWriteContents(context.Background(), testOther, testRepo)
	if err != nil {
		t.Fatalf("canWriteContents: %v", err)
	}
	if !can {
		t.Fatal("expected canWriteContents = true for write member")
	}
}

func TestCanWriteContents_ReadMember(t *testing.T) {
	members := &stubMemberStore{
		get: func(repoID, userID int64) (*domain.RepoMember, error) {
			return &domain.RepoMember{Role: domain.MemberRoleRead}, nil
		},
	}
	h := newTestHandler(testOther, nil, members, &stubUserRepo{}, &stubResolver{})
	can, err := h.canWriteContents(context.Background(), testOther, testRepo)
	if err != nil {
		t.Fatalf("canWriteContents: %v", err)
	}
	if can {
		t.Fatal("expected canWriteContents = false for read member")
	}
}

func TestCanManageRepo_NonOwner(t *testing.T) {
	h := newTestHandler(testOther, nil, &stubMemberStore{}, &stubUserRepo{}, &stubResolver{})
	can, err := h.canManageRepo(context.Background(), testOther, testRepo)
	if err != nil {
		t.Fatalf("canManageRepo: %v", err)
	}
	if can {
		t.Fatal("expected canManageRepo = false for non-owner")
	}
}

func TestCanManageRepo_Owner(t *testing.T) {
	h := newTestHandler(testOwner, nil, &stubMemberStore{}, &stubUserRepo{}, &stubResolver{})
	can, err := h.canManageRepo(context.Background(), testOwner, testRepo)
	if err != nil {
		t.Fatalf("canManageRepo: %v", err)
	}
	if !can {
		t.Fatal("expected canManageRepo = true for owner")
	}
}

// ------- domain tests -------

func TestMemberRole_Valid(t *testing.T) {
	if !domain.MemberRoleRead.Valid() {
		t.Fatal("MemberRoleRead.Valid() = false")
	}
	if !domain.MemberRoleWrite.Valid() {
		t.Fatal("MemberRoleWrite.Valid() = false")
	}
	if domain.MemberRole("admin").Valid() {
		t.Fatal("admin.Valid() = true, want false")
	}
	if domain.MemberRole("").Valid() {
		t.Fatal("empty.Valid() = true, want false")
	}
}

// TestGitCallerHasWriteScope pins the coarse push-authorization gate. In the
// contribution-branch model an agent session may push as long as it is bound
// to an issue with a role key; the actual namespace restriction is the per-ref
// ACL enforced in gitReceivePack (see TestSessionRefAllowed). PATs still need
// repo:write; cookie/password are full users.
func TestGitCallerHasWriteScope(t *testing.T) {
	num := func(n int32) *int32 { return &n }
	tests := []struct {
		name   string
		caller gitCaller
		want   bool
	}{
		{
			name:   "session bound to issue+role -> allowed",
			caller: gitCaller{authMethod: "session", session: &runnerdomain.AgentSession{IssueNumber: num(5), RoleKey: "server"}},
			want:   true,
		},
		{
			name:   "session with role but no issue -> blocked",
			caller: gitCaller{authMethod: "session", session: &runnerdomain.AgentSession{RoleKey: "server"}},
			want:   false,
		},
		{
			name:   "session with issue but no role -> blocked",
			caller: gitCaller{authMethod: "session", session: &runnerdomain.AgentSession{IssueNumber: num(5)}},
			want:   false,
		},
		{
			name:   "session nil -> blocked",
			caller: gitCaller{authMethod: "session", session: nil},
			want:   false,
		},
		{
			name:   "pat with repo:write scope -> allowed",
			caller: gitCaller{authMethod: "pat", token: &tokendomain.Token{Scopes: []tokendomain.Scope{tokendomain.ScopeRepoWrite}}},
			want:   true,
		},
		{
			name:   "pat with only repo:read scope -> blocked",
			caller: gitCaller{authMethod: "pat", token: &tokendomain.Token{Scopes: []tokendomain.Scope{tokendomain.ScopeRepoRead}}},
			want:   false,
		},
		{
			name:   "pat nil token -> blocked",
			caller: gitCaller{authMethod: "pat", token: nil},
			want:   false,
		},
		{
			name:   "cookie/password session -> full user, allowed",
			caller: gitCaller{authMethod: "password"},
			want:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.caller.hasWriteScope(); got != tc.want {
				t.Fatalf("hasWriteScope() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSessionRefAllowed pins the per-ref ACL: a session may only push to refs
// inside its own per-issue namespace refs/heads/issue-<N>/<role>[/...].
func TestSessionRefAllowed(t *testing.T) {
	num := func(n int32) *int32 { return &n }
	sess := &runnerdomain.AgentSession{IssueNumber: num(5), RoleKey: "server"}
	base := sessionNamespacePrefix(sess)
	if base != "refs/heads/issue-5/server" {
		t.Fatalf("sessionNamespacePrefix = %q, want refs/heads/issue-5/server", base)
	}
	tests := []struct {
		ref  string
		want bool
	}{
		{"refs/heads/issue-5/server", true},            // the namespace base
		{"refs/heads/issue-5/server/experiment", true}, // a sub-slug
		{"refs/heads/issue-5/web", false},              // another role's namespace
		{"refs/heads/issue/5", false},                  // the protected issue branch
		{"refs/heads/main", false},                     // base branch
		{"refs/heads/issue-50/server", false},          // different issue (prefix trap)
	}
	for _, tc := range tests {
		if got := refWithinNamespace(tc.ref, base); got != tc.want {
			t.Errorf("refWithinNamespace(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
	// An unbound session denies everything.
	if refWithinNamespace("refs/heads/issue-5/server", sessionNamespacePrefix(&runnerdomain.AgentSession{})) {
		t.Error("unbound session should deny all refs")
	}
}
