// Package domain declares the repository metadata model, the Store interface
// for persistence, and shared sentinel errors. Other modules depend only on
// this package; the Postgres implementation and filesystem helpers live in
// the sibling infra package.
package domain

import (
	"context"
	"errors"
	"path"
	"time"
)

// Visibility controls whether a repo is listable / readable by users other
// than the owner. Enforcement lives in the handler layer; the Store does not
// filter by visibility on its own (the caller decides).
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

func (v Visibility) Valid() bool {
	return v == VisibilityPublic || v == VisibilityPrivate
}

// OwnerKind names which kind of principal owns a repo. Duplicated in
// org/domain.OwnerKind on purpose — keeping repo/domain free of an org
// import lets the issue module pull in only repo/domain and still match
// the wire encoding ("user" / "org") used everywhere else.
type OwnerKind string

const (
	OwnerKindUser OwnerKind = "user"
	OwnerKindOrg  OwnerKind = "org"
)

func (k OwnerKind) Valid() bool { return k == OwnerKindUser || k == OwnerKindOrg }

// Repo is the canonical metadata for a single bare repository. OwnerName is
// denormalized from users.username or organizations.name (which one depends
// on OwnerKind); Store implementations populate it on read so handlers can
// build filesystem paths without an extra lookup.
type Repo struct {
	ID            int64
	OwnerKind     OwnerKind
	OwnerID       int64 // user.id when Kind==user, org.id when Kind==org
	OwnerName     string
	Name          string
	Description   string
	Visibility    Visibility
	DefaultBranch string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ErrRepoNotFound is returned by Store lookups when no row matches.
var ErrRepoNotFound = errors.New("repo not found")

// ErrRepoConflict is returned when (owner, name) uniqueness is violated.
var ErrRepoConflict = errors.New("repo already exists")

// ErrInvalidName is returned when a supplied repository name fails validation.
var ErrInvalidName = errors.New("invalid repo name")

// ErrInvalidOwnerKind is returned by Store implementations when the caller
// passes an OwnerKind other than the two declared constants.
var ErrInvalidOwnerKind = errors.New("invalid owner kind")

// ErrProtectionNotFound is returned when a branch_protections lookup misses.
var ErrProtectionNotFound = errors.New("branch protection not found")

// ErrProtectionConflict is returned when (repo_id, pattern) is already taken.
var ErrProtectionConflict = errors.New("branch protection pattern already exists")

// BranchProtection is a single rule for one ref-name pattern (glob-style via
// filepath.Match). All three forbid_* flags are independent — a rule may
// enforce any subset of them.
type BranchProtection struct {
	ID               int64
	RepoID           int64
	Pattern          string
	ForbidForcePush  bool
	ForbidDelete     bool
	ForbidDirectPush bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ProtectionStore is the persistence abstraction for branch_protections.
// The handler scopes every call to a single repo via repoID; the Postgres
// implementation enforces (repo_id, pattern) uniqueness and maps the
// violation to ErrProtectionConflict.
type ProtectionStore interface {
	List(ctx context.Context, repoID int64) ([]*BranchProtection, error)
	Get(ctx context.Context, id, repoID int64) (*BranchProtection, error)
	Create(ctx context.Context, repoID int64, pattern string, forbidForcePush, forbidDelete, forbidDirectPush bool) (*BranchProtection, error)
	Update(ctx context.Context, id, repoID int64, pattern string, forbidForcePush, forbidDelete, forbidDirectPush bool) (*BranchProtection, error)
	Delete(ctx context.Context, id, repoID int64) error
}

// MatchProtection returns the first rule in the slice whose Pattern matches
// branchName (filepath.Match semantics: `*` matches a segment, `?` matches
// one char). Order is stable on Pattern (the store returns ORDER BY pattern),
// so callers get deterministic behavior when multiple rules overlap. A bad
// pattern in the DB is silently skipped — patterns are validated on the way
// in, and we don't want a single bad row to wedge every subsequent push.
func MatchProtection(rules []*BranchProtection, branchName string) *BranchProtection {
	for _, r := range rules {
		ok, err := path.Match(r.Pattern, branchName)
		if err == nil && ok {
			return r
		}
	}
	return nil
}

// PathResolver is the narrow filesystem-path contract that cross-module
// callers depend on instead of importing the concrete *infra.Storage.
// ResolvePath validates the two path components against the same
// fs-safety regex the create flow uses; an unsafe component returns a
// sentinel error so callers can 400 / 404 rather than 500.
type PathResolver interface {
	ResolvePath(ownerUsername, repoName string) (string, error)
}

// Store is the persistence abstraction for repo metadata. The owner is
// addressed as a (kind, id) pair: kind tells the implementation whether to
// store the id in owner_user_id or owner_org_id. Implementations must map
// the Postgres unique-violation on either (owner_user_id, name) or
// (owner_org_id, name) to ErrRepoConflict and missing-row lookups to
// ErrRepoNotFound.
type Store interface {
	Create(ctx context.Context, ownerKind OwnerKind, ownerID int64, name, description, defaultBranch string, visibility Visibility) (*Repo, error)
	GetByID(ctx context.Context, id int64) (*Repo, error)
	GetByOwnerAndName(ctx context.Context, ownerKind OwnerKind, ownerID int64, name string) (*Repo, error)
	ListByOwner(ctx context.Context, ownerKind OwnerKind, ownerID int64, includePrivate bool, offset, limit int32) ([]*Repo, int64, error)
	Delete(ctx context.Context, id int64) error
	UpdateMeta(ctx context.Context, id int64, description, defaultBranch string, visibility Visibility) (*Repo, error)
	Transfer(ctx context.Context, id int64, newOwnerKind OwnerKind, newOwnerID int64) (*Repo, error)
}
