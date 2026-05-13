// Package domain declares the repository metadata model, the Store interface
// for persistence, and shared sentinel errors. Other modules depend only on
// this package; the Postgres implementation and filesystem helpers live in
// the sibling infra package.
package domain

import (
	"context"
	"errors"
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
