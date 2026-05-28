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

// MemberRole is the per-repo role of a single user. Two-tier:
// write (can push, write content) / read (can view only).
// owner does not appear in repo_members — they are expressed
// via repos.owner_user_id / owner_org_id and implicitly have
// maximum permission.
type MemberRole string

const (
	MemberRoleWrite MemberRole = "write"
	MemberRoleRead  MemberRole = "read"
)

func (r MemberRole) Valid() bool { return r == MemberRoleWrite || r == MemberRoleRead }

// RepoMember is one row in repo_members. Role is the user's role
// within the repo. AddedBy records which user issued the add (for audit).
type RepoMember struct {
	RepoID   int64
	UserID   int64
	Username string
	Role     MemberRole
	AddedBy  int64
	AddedAt  time.Time
}

// Errors for repo_members operations.
var (
	ErrRepoMemberNotFound  = errors.New("repo member not found")
	ErrRepoMemberConflict  = errors.New("user is already a repo member")
	ErrOrgRepoNotSupported = errors.New("repo members are not supported on org-owned repos")
)

// MemberStore is the persistence abstraction for repo_members.
type MemberStore interface {
	AddMember(ctx context.Context, repoID, userID, actorID int64, role MemberRole) error
	UpdateMemberRole(ctx context.Context, repoID, userID int64, role MemberRole) error
	RemoveMember(ctx context.Context, repoID, userID int64) error
	ListMembers(ctx context.Context, repoID int64) ([]*RepoMember, error)
	GetMember(ctx context.Context, repoID, userID int64) (*RepoMember, error)
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

// VariableKind distinguishes plain variables (readable by anyone with
// manage access) from secret variables (write-only after creation).
type VariableKind string

const (
	VariableKindPlain  VariableKind = "plain"
	VariableKindSecret VariableKind = "secret"
)

func (k VariableKind) Valid() bool { return k == VariableKindPlain || k == VariableKindSecret }

// RepoVariable is one row in repo_variables.
type RepoVariable struct {
	ID     int64
	RepoID int64
	Name   string
	Value  string // ciphertext when kind=secret; plaintext when kind=plain
	Kind   VariableKind
	// DecryptionFailed is true when the stored ciphertext could not be
	// decrypted (e.g. key rotation, corruption). The Value field is ""
	// and the entry must not be used for ${NAME} expansion.  Callers
	// that forward variables to the runner MUST skip these entries.
	DecryptionFailed bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RepoVariablePublic is the API-safe projection. For secret variables,
// Value is always empty; the caller must re-submit a plaintext to update.
type RepoVariablePublic struct {
	ID        int64        `json:"id"`
	RepoID    int64        `json:"repo_id"`
	Name      string       `json:"name"`
	Value     string       `json:"value,omitempty"`
	Kind      VariableKind `json:"kind"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// Errors for repo_variables operations.
var (
	ErrVariableNotFound         = errors.New("repo variable not found")
	ErrVariableNameEmpty        = errors.New("variable name must not be empty")
	ErrVariableNameInvalid      = errors.New("variable name must match [A-Z_][A-Z0-9_]*")
	ErrVariableConflict         = errors.New("a variable with that name already exists")
	ErrVariableKindInvalid      = errors.New("variable kind must be 'plain' or 'secret'")
	ErrVariableDecryptionFailed = errors.New("variable decryption failed")
)

// VariableStore is the persistence abstraction for repo_variables.
// Secret values are encrypted/decrypted by the infra layer; the domain
// contract is plaintext in, plaintext out — callers never see ciphertext.
type VariableStore interface {
	List(ctx context.Context, repoID int64) ([]*RepoVariable, error)
	Get(ctx context.Context, id, repoID int64) (*RepoVariable, error)
	Create(ctx context.Context, repoID int64, name, value string, kind VariableKind) (*RepoVariable, error)
	Update(ctx context.Context, id, repoID int64, name, value string, kind VariableKind) (*RepoVariable, error)
	Delete(ctx context.Context, id, repoID int64) error
}
