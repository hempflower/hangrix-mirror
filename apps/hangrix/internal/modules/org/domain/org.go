// Package domain declares the organization model, role types, persistence
// interfaces, and the cross-module owner Resolver. Other modules depend only
// on this package; the Postgres implementation lives in the sibling infra
// package and the HTTP surface in handler.
//
// Owner namespace: organizations and users share a single name namespace.
// Both `users.username` and `organizations.name` are unique within their own
// table; in addition, no name may appear in both tables, and no name may
// match a reserved system identifier (see IsReservedName).
package domain

import (
	"context"
	"errors"
	"time"
)

// OwnerKind names which kind of principal owns a resource (currently only
// repositories). Duplicated in repo/domain for the same reason — keeping the
// repo module free of an org/domain import. Both spellings round-trip on the
// wire as the bare string "user" / "org".
type OwnerKind string

const (
	OwnerKindUser OwnerKind = "user"
	OwnerKindOrg  OwnerKind = "org"
)

func (k OwnerKind) Valid() bool { return k == OwnerKindUser || k == OwnerKindOrg }

// Role is the per-org role of a single user. Two-tier on purpose — adding
// teams / repo-level roles is M10+ territory.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

func (r Role) Valid() bool { return r == RoleOwner || r == RoleMember }

// Org is the canonical organization metadata. Soft-deleted via DeletedAt
// (the row stays for audit) — mirrors the user.disabled pattern.
type Org struct {
	ID          int64
	Name        string
	DisplayName string
	Description string
	AvatarURL   string
	CreatedBy   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Membership is one row in organization_members. Role is the user's role
// within Org. AddedBy records which user issued the add (for audit); equal
// to CreatedBy on org-creation membership.
type Membership struct {
	OrgID    int64
	UserID   int64
	Username string
	Role     Role
	AddedBy  int64
	AddedAt  time.Time
}

// Owner is the resolved (kind, id, name) tuple a caller passes around after
// calling Resolver.ResolveOwner. It is the only sanctioned way for other
// modules to translate a path-segment "owner" string into a database id.
type Owner struct {
	Kind OwnerKind
	ID   int64
	Name string
}

// Errors.
var (
	ErrOrgNotFound    = errors.New("organization not found")
	ErrOrgConflict    = errors.New("organization name already taken")
	ErrOrgReserved    = errors.New("organization name is reserved")
	ErrMemberNotFound = errors.New("member not found")
	ErrMemberConflict = errors.New("member already in organization")
	ErrLastOwner      = errors.New("cannot remove or demote the last owner")
	ErrOwnerNotFound  = errors.New("owner not found")
	ErrInvalidOrgName = errors.New("invalid organization name")
)

// OrgRepo is the persistence abstraction for organization rows and their
// membership table. The Postgres implementation maps unique-violation on
// `name` to ErrOrgConflict and missing rows to ErrOrgNotFound /
// ErrMemberNotFound.
type OrgRepo interface {
	Create(ctx context.Context, name, displayName, description string, actorID int64) (*Org, error)
	GetByName(ctx context.Context, name string) (*Org, error)
	GetByID(ctx context.Context, id int64) (*Org, error)
	Exists(ctx context.Context, name string) (bool, error)
	UpdateMeta(ctx context.Context, id int64, displayName, description, avatarURL string) (*Org, error)
	SoftDelete(ctx context.Context, id int64) error

	AddMember(ctx context.Context, orgID, userID, actorID int64, role Role) error
	UpdateMemberRole(ctx context.Context, orgID, userID int64, role Role) error
	RemoveMember(ctx context.Context, orgID, userID int64) error
	ListMembers(ctx context.Context, orgID int64) ([]*Membership, error)
	GetMember(ctx context.Context, orgID, userID int64) (*Membership, error)
	CountOwners(ctx context.Context, orgID int64) (int64, error)
	ListOrgsForUser(ctx context.Context, userID int64) ([]*Org, error)
}

// Resolver is the cross-module entry point. Callers (repo, issue handlers)
// use it to:
//
//   - Translate a path-segment "owner" string into an Owner struct
//     (Kind + ID + Name). Looks up users first, then orgs; ErrOwnerNotFound
//     if neither matches.
//   - Resolve a user's role inside an org for access checks. Returns
//     (Role, true, nil) for a member, ("", false, nil) for a non-member,
//     or (_, _, err) on infra failure.
//
// Living in org/domain keeps repo/domain free of an org-package import while
// still giving handlers a single object to depend on for owner-aware
// behaviors.
type Resolver interface {
	ResolveOwner(ctx context.Context, name string) (*Owner, error)
	Membership(ctx context.Context, orgID, userID int64) (Role, bool, error)
}
