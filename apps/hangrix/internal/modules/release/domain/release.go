// Package domain declares the release model, the Store interface
// for persistence, and sentinel errors.
package domain

import (
	"context"
	"errors"
	"time"
)

// Release is the canonical metadata for a single release.
type Release struct {
	ID              int64
	RepoID          int64
	TagName         string
	TargetCommitSHA string
	Title           string
	Notes           string
	IsDraft         bool
	PublishedAt     time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Asset is one custom uploaded asset attached to a release.
type Asset struct {
	ID          int64
	ReleaseID   int64
	Name        string
	ContentType string
	SizeBytes   int64
	StorageKey  string
	CreatedAt   time.Time
}

// Errors for release operations.
var (
	ErrReleaseNotFound = errors.New("release not found")
	ErrReleaseConflict = errors.New("a release for this tag already exists")
	ErrReleaseNotDraft = errors.New("release is not in draft state")
	ErrTagNotFound     = errors.New("tag not found")
	ErrAssetNotFound   = errors.New("asset not found")
	ErrAssetConflict   = errors.New("an asset with this name already exists")
	ErrAssetNameEmpty  = errors.New("asset name must not be empty")
)

// Store is the persistence abstraction for releases.
type Store interface {
	Create(ctx context.Context, repoID int64, tagName, targetCommitSHA, title, notes string) (*Release, error)
	GetByID(ctx context.Context, id int64) (*Release, error)
	GetByRepoAndTag(ctx context.Context, repoID int64, tagName string) (*Release, error)
	ListByRepo(ctx context.Context, repoID int64, offset, limit int32, draft *bool) ([]*Release, int64, error)
	Update(ctx context.Context, id int64, tagName, targetCommitSHA, title, notes string) (*Release, error)
	Publish(ctx context.Context, id int64) (*Release, error)
	Delete(ctx context.Context, id int64) error
}

// AssetStore is the persistence abstraction for release assets.
type AssetStore interface {
	Create(ctx context.Context, releaseID int64, name, contentType string, sizeBytes int64, storageKey string) (*Asset, error)
	GetByID(ctx context.Context, id int64) (*Asset, error)
	ListByRelease(ctx context.Context, releaseID int64) ([]*Asset, error)
	Delete(ctx context.Context, id int64) error
}
