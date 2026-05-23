// Package domain declares the Git read/init interface and shared value types.
// Implementations live in infra. Consumers (notably the repo module) depend
// only on this package.
package domain

import (
	"errors"
	"time"
)

// Git operates on bare repositories rooted at a filesystem path. All methods
// are read-only except Init and SeedInitialCommit; nothing here writes ref
// updates beyond the initial seed commit. Push lives in M3.
type Git interface {
	// Init creates a bare repository at path with the given default branch
	// name (no leading "refs/heads/"). The directory is created if missing.
	// Returns nil if the path already contains a bare repo.
	Init(path string, defaultBranch string) error

	// SeedInitialCommit writes a single initial commit containing every
	// (path, body) pair in files. The bare repo MUST be otherwise empty
	// — call resolves to a no-op when the default branch already has
	// commits. Author identifies the human who triggered the create; the
	// committer is set to the same identity. Used so freshly-created
	// repos can be cloned without first needing M3 push. Paths are
	// repo-relative, forward-slash, no leading "./"; nested directories
	// (e.g. ".hangrix/agents.yml") work without an explicit "tree"
	// entry — the implementation builds the nested tree structure from
	// the slash-separated keys.
	SeedInitialCommit(path, defaultBranch string, files map[string][]byte, authorName, authorEmail string) error

	// ListRefs returns branch and tag heads, plus the repo's resolved
	// default-branch SHA (empty string when the repo has no commits).
	ListRefs(path string) (*Refs, error)

	// ListCommits walks history from ref backwards, returning at most limit
	// commits after skipping offset. ref may be a branch name, tag, or SHA.
	// Returns ErrEmptyRepo when the repo has no commits.
	ListCommits(path, ref string, offset, limit int) ([]*Commit, error)

	// CommitByID returns a single commit and the diff against its first
	// parent (empty diff for root commits).
	CommitByID(path, sha string) (*CommitWithDiff, error)

	// Tree returns the entries directly under treePath at refOrSha. Pass
	// "" for treePath to list the root.
	Tree(path, refOrSha, treePath string) ([]*TreeEntry, error)

	// TreeView returns the same tree as Tree(), but each entry is enriched
	// with the most recent commit that touched its path, plus a top-level
	// "last commit" and total commit count for refOrSha. This powers the
	// GitHub-style file browser. Walks the commit graph until every entry
	// has been assigned or the log is exhausted, so it's O(commits *
	// changed_files) in the worst case — fine for M3 scale.
	TreeView(path, refOrSha, treePath string) (*TreeView, error)

	// Blob returns the raw bytes of the file at refOrSha:filePath, plus a
	// best-effort binary flag (true iff any of the first 8KB is a NUL byte).
	Blob(path, refOrSha, filePath string) (content []byte, binary bool, err error)

	// DiffRefs computes the diff "from..to" (i.e. what changed going from
	// from to to). Both arguments may be branches, tags, or SHAs.
	DiffRefs(path, from, to string) ([]*FileDiff, error)

	// DiffMergeBase computes the diff from the merge-base of base and topic
	// to topic itself — i.e. what changed on topic since it diverged from
	// base. This is the equivalent of `git diff base...topic` (three-dot
	// diff) and ensures only the topic's own changes appear, even when
	// base has moved forward with unrelated work.
	DiffMergeBase(path, base, topic string) ([]*FileDiff, error)

	// ---- Write operations (M3) ----

	// CreateBranch points branchName at startRef. Returns ErrBranchExists
	// if branchName already resolves; ErrRefNotFound if startRef can't be
	// resolved.
	CreateBranch(path, branchName, startRef string) error

	// CreateBranchAt points branchName directly at commitSHA without resolving
	// a ref name first. Returns ErrBranchExists if branchName already exists;
	// ErrCommitNotFound if commitSHA cannot be resolved. Use this when you
	// already have a validated commit SHA and want to avoid a re-resolution
	// race (e.g. new-branch path in online edit).
	CreateBranchAt(path, branchName, commitSHA string) error

	// DeleteBranch removes the branch ref. Returns ErrRefNotFound when the
	// branch doesn't exist and ErrCannotDeleteHEAD when the branch is the
	// current HEAD (caller should switch HEAD first via SetHEAD).
	DeleteBranch(path, branchName string) error

	// SetHEAD points the symbolic ref HEAD at refs/heads/branchName.
	// branchName must already exist as a branch (use CreateBranch first if
	// you're switching default to a new line). Returns ErrRefNotFound if
	// missing.
	SetHEAD(path, branchName string) error

	// CreateLightweightTag points tagName at refOrSha. No tag object is
	// written — the ref directly addresses the commit/object. Returns
	// ErrTagExists if a tag of that name already exists.
	CreateLightweightTag(path, tagName, refOrSha string) error

	// CreateAnnotatedTag writes a tag object with message + tagger and
	// points tagName at it. tagger.When may be zero — implementations
	// fill in time.Now() in that case.
	CreateAnnotatedTag(path, tagName, refOrSha, message string, tagger Signature) error

	// DeleteTag removes the tag ref (and orphans the underlying tag object
	// for annotated tags — gc cleans up eventually). Returns ErrRefNotFound
	// if missing.
	DeleteTag(path, tagName string) error

	// ContainsCommit returns the branch and tag refs whose tip commits have
	// sha as an ancestor (or equal to sha). Equivalent to the union of
	// `git branch --contains <sha>` and `git tag --contains <sha>`. Returns
	// ErrRefNotFound if sha cannot be resolved.
	ContainsCommit(path, sha string) (*ContainingRefs, error)

	// IsAncestor reports whether ancestor is reachable from descendant via
	// the parent chain (i.e. fast-forward is possible from ancestor to
	// descendant). Both inputs may be any ref or SHA. ErrRefNotFound if
	// either cannot be resolved.
	IsAncestor(path, ancestor, descendant string) (bool, error)

	// CheckFastForward reports whether headRef is a descendant of baseRef
	// (i.e. baseRef can be fast-forward merged into headRef). Returns
	// (isFF, mode, error). mode is "fast-forward" when baseRef is an
	// ancestor, "diverged" when there is no ancestor relationship, or
	// "unknown" when either ref cannot be resolved. Both inputs may be
	// any ref or SHA. A zero-value headRef (empty string) means the
	// branch has no commits yet — returns (false, "unknown", nil).
	CheckFastForward(path, baseRef, headRef string) (bool, string, error)


	// ResolveCommit returns the commit SHA the ref resolves to. Empty string
	// (no error) is reserved for the "unborn branch" case so callers can
	// branch on it. ErrRefNotFound when the ref does not exist at all.
	ResolveCommit(path, ref string) (string, error)

	// MergeBranch merges fromRef into intoBranch using a merge-commit strategy:
	//   - intoBranch is unborn → intoBranch is created pointing at fromRef
	//     (mode "fast-forward").
	//   - intoBranch == fromRef commit → no-op (mode "up-to-date").
	//   - fromRef is an ancestor of intoBranch → no-op (mode "up-to-date").
	//   - intoBranch is an ancestor of fromRef → intoBranch is advanced to
	//     fromRef (mode "fast-forward").
	//   - otherwise → a merge commit is created with parents (intoBranch,
	//     fromRef) using the merge-base for a three-way tree merge. If the
	//     tree merge conflicts, ErrMergeConflict is returned. On success the
	//     base branch ref (intoBranch) is updated to the merge commit; the
	//     issue branch ref (fromRef) is left unchanged, preserving the
	//     original history (mode "merge-commit").
	//
	// Returns the resulting commit SHA on intoBranch, the mode, and any error.
	MergeBranch(path, intoBranch, fromRef, message string, author Signature) (sha, mode string, err error)
	
	// CheckAutoMerge evaluates whether headRef can be merged into baseRef
	// using a merge-commit strategy, without modifying any refs. Returns:
	//
	//   - mergeable=true, mode="fast-forward": baseRef is an ancestor of
	//     headRef (direct fast-forward possible).
	//   - mergeable=true, mode="up-to-date": headRef is already reachable
	//     from baseRef (nothing to merge).
	//   - mergeable=true, mode="merge-commit": branches have diverged but
	//     a three-way tree merge between baseRef and headRef would succeed
	//     without conflicts.
	//   - mergeable=false, mode="conflicted": the three-way merge would
	//     produce conflicts that require manual resolution.
	//   - mergeable=false, mode="unknown": one or both refs cannot be
	//     resolved, or headRef is empty.
	//
	// The hint provides a human-readable explanation of the result.
	// This is the shared pre-flight check used by both issue_mergeable
	// and issue_merge.
	CheckAutoMerge(path, baseRef, headRef string) (mergeable bool, mode string, hint string, err error)

	// ApplyPatch applies a unified diff patchText onto branch at path,
	// creating a new commit with the given message. The author identifies
	// the original patch submitter; the committer identifies who is
	// applying it. Returns the new commit SHA. Fails if the patch does not
	// apply cleanly (the underlying git apply would reject it) or the
	// branch cannot be resolved.
	ApplyPatch(path, branch, patchText, message string, author, committer Signature) (sha string, err error)

	// EditAndCommit replaces the blob at filePath in the tree of the HEAD
	// commit of branch with newContent, builds a new tree, creates a
	// commit with the given message, and advances the branch ref to it.
	//
	// baseCommitSHA is the commit the caller believes is the branch tip;
	// the method uses an atomic compare-and-swap on the branch ref —
	// the ref is only advanced if it still points at baseCommitSHA.
	// If the ref has moved, ErrRefChanged is returned.
	//
	// filePath is repo-relative, forward-slash separated (e.g. "docs/intro.md").
	// newContent is the complete new file content as raw bytes (UTF-8 text).
	// author and committer identify who made the change.
	//
	// Returns the new commit SHA. Possible errors: ErrRepoNotFound,
	// ErrRefNotFound (branch doesn't exist or has no commits),
	// ErrPathNotFound (filePath not in tree), ErrNotABlob (path exists
	// but is not a regular file), ErrRefChanged (concurrent write).
	EditAndCommit(path, branch, baseCommitSHA, filePath string, newContent []byte, message string, author, committer Signature) (newCommitSHA string, err error)
}

// JSON tags are intentionally on these domain types because the repo handler
// serializes them directly to clients (no separate DTO layer for read paths).
// Tags don't leak transport concerns into the interface — only into the value
// shape — so this is a deliberate pragma, not a tier crossing.

type Refs struct {
	DefaultBranch    string `json:"default_branch"`     // resolved default-branch name (e.g. "main")
	DefaultBranchSHA string `json:"default_branch_sha"` // empty when repo has no commits
	Branches         []*Ref `json:"branches"`
	Tags             []*Ref `json:"tags"`
}

// ContainingRefs is the answer to "which refs contain this commit?": the
// branch and tag names whose tip has the queried commit as an ancestor (or
// is that commit). Lightweight and annotated tags are mixed in the same Tags
// slice — at this UI level the distinction doesn't matter.
type ContainingRefs struct {
	Branches []*Ref `json:"branches"`
	Tags     []*Ref `json:"tags"`
}

type Ref struct {
	Name string `json:"name"` // e.g. "main", "v1.0"
	SHA  string `json:"sha"`
}

type Signature struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	When  time.Time `json:"when"`
}

type Commit struct {
	SHA         string    `json:"sha"`
	ParentSHAs  []string  `json:"parent_shas"`
	Author      Signature `json:"author"`
	Committer   Signature `json:"committer"`
	Message     string    `json:"message"`
	CommittedAt time.Time `json:"committed_at"`
}

type CommitWithDiff struct {
	Commit *Commit     `json:"commit"`
	Diff   []*FileDiff `json:"diff"`
}

// TreeEntry kinds. Symlink and Submodule kept distinct from Blob so the UI
// can render them differently if it wants to.
const (
	EntryKindBlob       = "blob"
	EntryKindExecutable = "executable"
	EntryKindTree       = "tree"
	EntryKindSymlink    = "symlink"
	EntryKindSubmodule  = "submodule"
)

type TreeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"` // full path relative to repo root
	Kind string `json:"kind"` // one of EntryKind*
	SHA  string `json:"sha"`
	Size int64  `json:"size"` // 0 for non-blob entries
}

// EntryWithCommit is a TreeEntry plus the most recent commit that touched
// the entry's path. LastCommit can be nil only if the walk exhausted before
// the entry was assigned — usually a bug, but tolerated so the UI can fall
// back to "—".
type EntryWithCommit struct {
	*TreeEntry
	LastCommit *Commit `json:"last_commit,omitempty"`
}

// TreeView wraps an enriched directory listing with header info: the most
// recent commit on refOrSha (the "what's the latest" line in a GitHub-style
// file browser) and the total commit count on that ref.
type TreeView struct {
	Entries      []*EntryWithCommit `json:"entries"`
	LastCommit   *Commit            `json:"last_commit,omitempty"` // nil only for empty repos
	TotalCommits int64              `json:"total_commits"`
}

// FileDiff statuses.
const (
	DiffStatusAdded    = "added"
	DiffStatusModified = "modified"
	DiffStatusDeleted  = "deleted"
	DiffStatusRenamed  = "renamed"
)

type FileDiff struct {
	OldPath string `json:"old_path"` // empty when status=added
	NewPath string `json:"new_path"` // empty when status=deleted
	Status  string `json:"status"`   // one of DiffStatus*
	Patch   string `json:"patch"`    // unified diff text; may be empty for binary diffs
	Binary  bool   `json:"binary"`
}

var (
	ErrRepoNotFound     = errors.New("git: repository not found at path")
	ErrRefNotFound      = errors.New("git: ref not found")
	ErrPathNotFound     = errors.New("git: path not found in tree")
	ErrNotABlob         = errors.New("git: path is not a blob")
	ErrEmptyRepo        = errors.New("git: repository has no commits")
	ErrBranchExists     = errors.New("git: branch already exists")
	ErrTagExists        = errors.New("git: tag already exists")
	ErrCannotDeleteHEAD = errors.New("git: cannot delete current HEAD branch")
	ErrInvalidRefName   = errors.New("git: invalid ref name")
	ErrMergeConflict = errors.New("git: merge conflict")
	ErrRefChanged    = errors.New("git: ref has changed concurrently")
)
