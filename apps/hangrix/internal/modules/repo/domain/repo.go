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

// Repo is the canonical metadata for a single bare repository. OwnerUsername
// is denormalized from the users table; Store implementations populate it on
// read so handlers can build filesystem paths without an extra lookup.
type Repo struct {
	ID            int64
	OwnerID       int64
	OwnerUsername string
	Name          string
	Description   string
	Visibility    Visibility
	DefaultBranch string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ErrRepoNotFound is returned by Store lookups when no row matches.
var ErrRepoNotFound = errors.New("repo not found")

// ErrRepoConflict is returned when (owner_id, name) uniqueness is violated.
var ErrRepoConflict = errors.New("repo already exists")

// ErrInvalidName is returned when a supplied repository name fails validation.
var ErrInvalidName = errors.New("invalid repo name")

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

// Store is the persistence abstraction for repo metadata. Implementations
// must map the Postgres unique-violation on (owner_id, name) to
// ErrRepoConflict and missing-row lookups to ErrRepoNotFound.
type Store interface {
	Create(ctx context.Context, ownerID int64, name, description, defaultBranch string, visibility Visibility) (*Repo, error)
	GetByID(ctx context.Context, id int64) (*Repo, error)
	GetByOwnerAndName(ctx context.Context, ownerID int64, name string) (*Repo, error)
	ListByOwner(ctx context.Context, ownerID int64, includePrivate bool, offset, limit int32) ([]*Repo, int64, error)
	Delete(ctx context.Context, id int64) error
	UpdateMeta(ctx context.Context, id int64, description, defaultBranch string, visibility Visibility) (*Repo, error)
}
