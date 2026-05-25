// Package infra holds the go-git-backed concrete implementation of the git
// domain. GoGit operates on bare repositories at filesystem paths; it has no
// dependencies beyond go-git itself. Other modules consume it via domain.Git
// resolved through the ioc container.
package infra

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	fdiff "github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
)

// GoGit is a stateless implementation of domain.Git backed by github.com/go-git/go-git.
// All state lives on disk in the bare repositories under the configured root;
// this struct exists only to bind methods to a value the DI container can hand out.
type GoGit struct{}

// GoGitDeps is the empty deps holder required by the project's DI convention.
// See pkg/ioc/module.go: every constructor takes a *Deps pointer to a struct,
// even when there are no dependencies to inject.
type GoGitDeps struct{}

// NewGoGit constructs a GoGit. It has no side effects.
func NewGoGit(_ *GoGitDeps) *GoGit { return &GoGit{} }

// Init creates a bare repository at path and points HEAD at the given default
// branch. If the path already contains a bare repo, Init is a no-op.
func (g *GoGit) Init(path string, defaultBranch string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("init: mkdir parent: %w", err)
	}

	_, err := git.PlainInit(path, true)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryAlreadyExists) {
			return nil
		}
		return fmt.Errorf("init: plain init: %w", err)
	}

	repo, err := git.PlainOpen(path)
	if err != nil {
		return fmt.Errorf("init: reopen: %w", err)
	}
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch))
	if err := repo.Storer.SetReference(headRef); err != nil {
		return fmt.Errorf("init: set HEAD: %w", err)
	}
	return nil
}

// SeedInitialCommit writes one initial commit containing every entry in
// files (keyed by repo-relative slash-separated path), then advances
// refs/heads/<defaultBranch> to it. No-op when the branch already has
// commits. Nested paths (e.g. ".hangrix/agents.yml") are split on "/" so
// the necessary tree objects are materialised automatically.
func (g *GoGit) SeedInitialCommit(path, defaultBranch string, files map[string][]byte, authorName, authorEmail string) error {
	if len(files) == 0 {
		return fmt.Errorf("seed: no files supplied")
	}
	repo, err := openRepo(path)
	if err != nil {
		return err
	}

	branchRef := plumbing.NewBranchReferenceName(defaultBranch)
	if _, err := repo.Reference(branchRef, false); err == nil {
		return nil
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("seed: check branch: %w", err)
	}

	st := repo.Storer

	// Step 1: write every file body as a blob, remember each by its
	// slash-split path so the tree-builder can walk by directory.
	type fileEntry struct {
		segments []string
		hash     plumbing.Hash
	}
	entries := make([]fileEntry, 0, len(files))
	for relPath, body := range files {
		if relPath == "" || strings.HasPrefix(relPath, "/") || strings.Contains(relPath, "..") {
			return fmt.Errorf("seed: bad path %q", relPath)
		}
		blobObj := st.NewEncodedObject()
		blobObj.SetType(plumbing.BlobObject)
		blobObj.SetSize(int64(len(body)))
		w, err := blobObj.Writer()
		if err != nil {
			return fmt.Errorf("seed: blob writer (%s): %w", relPath, err)
		}
		if _, err := w.Write(body); err != nil {
			_ = w.Close()
			return fmt.Errorf("seed: write blob (%s): %w", relPath, err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("seed: close blob (%s): %w", relPath, err)
		}
		blobHash, err := st.SetEncodedObject(blobObj)
		if err != nil {
			return fmt.Errorf("seed: store blob (%s): %w", relPath, err)
		}
		entries = append(entries, fileEntry{
			segments: strings.Split(relPath, "/"),
			hash:     blobHash,
		})
	}

	// Step 2: build the root tree by recursing over the entries. Group
	// by first path segment; for groups whose member paths are deeper
	// than one segment, recurse to build a subtree blob, then point a
	// tree entry at it. Sort children alphabetically so the resulting
	// tree hash is deterministic for the same input.
	var buildTree func(prefix string, group []fileEntry) (plumbing.Hash, error)
	buildTree = func(prefix string, group []fileEntry) (plumbing.Hash, error) {
		// Bucket by the next segment.
		buckets := map[string][]fileEntry{}
		var leaves []object.TreeEntry
		for _, e := range group {
			if len(e.segments) == 1 {
				leaves = append(leaves, object.TreeEntry{
					Name: e.segments[0],
					Mode: filemode.Regular,
					Hash: e.hash,
				})
				continue
			}
			head := e.segments[0]
			buckets[head] = append(buckets[head], fileEntry{
				segments: e.segments[1:],
				hash:     e.hash,
			})
		}
		treeEntries := append([]object.TreeEntry(nil), leaves...)
		for name, child := range buckets {
			subHash, err := buildTree(prefix+name+"/", child)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			treeEntries = append(treeEntries, object.TreeEntry{
				Name: name,
				Mode: filemode.Dir,
				Hash: subHash,
			})
		}
		sort.Slice(treeEntries, func(i, j int) bool {
			return treeEntries[i].Name < treeEntries[j].Name
		})
		treeObj := st.NewEncodedObject()
		tree := &object.Tree{Entries: treeEntries}
		if err := tree.Encode(treeObj); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("seed: encode tree (%s): %w", prefix, err)
		}
		h, err := st.SetEncodedObject(treeObj)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("seed: store tree (%s): %w", prefix, err)
		}
		return h, nil
	}

	rootHash, err := buildTree("", entries)
	if err != nil {
		return err
	}

	// Step 3: write the commit pointing at the root tree.
	now := time.Now()
	sig := object.Signature{Name: authorName, Email: authorEmail, When: now}
	commit := &object.Commit{
		Author:    sig,
		Committer: sig,
		Message:   "Initial commit\n",
		TreeHash:  rootHash,
	}
	commitObj := st.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		return fmt.Errorf("seed: encode commit: %w", err)
	}
	commitHash, err := st.SetEncodedObject(commitObj)
	if err != nil {
		return fmt.Errorf("seed: store commit: %w", err)
	}

	if err := st.SetReference(plumbing.NewHashReference(branchRef, commitHash)); err != nil {
		return fmt.Errorf("seed: set branch ref: %w", err)
	}
	return nil
}

// ListRefs walks all repository references, splitting into branches and tags,
// and returns the resolved default branch and its current SHA.
func (g *GoGit) ListRefs(path string) (*domain.Refs, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, err
	}

	// Initialize slices so the JSON response is `[]` rather than `null` for
	// an empty repo — the web client treats the response as an array.
	out := &domain.Refs{
		Branches: []*domain.Ref{},
		Tags:     []*domain.Ref{},
	}

	// Default branch comes from the HEAD symref target.
	headRef, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return nil, fmt.Errorf("list refs: head: %w", err)
	}
	if headRef.Type() == plumbing.SymbolicReference {
		target := headRef.Target()
		if target.IsBranch() {
			out.DefaultBranch = target.Short()
		}
	}

	iter, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("list refs: iter: %w", err)
	}
	defer iter.Close()

	err = iter.ForEach(func(ref *plumbing.Reference) error {
		// Resolve symbolic refs to a hash; skip those we cannot resolve.
		if ref.Type() == plumbing.SymbolicReference {
			return nil
		}
		name := ref.Name()
		switch {
		case name.IsBranch():
			out.Branches = append(out.Branches, &domain.Ref{
				Name: name.Short(),
				SHA:  ref.Hash().String(),
			})
			if out.DefaultBranch != "" && name.Short() == out.DefaultBranch {
				out.DefaultBranchSHA = ref.Hash().String()
			}
		case name.IsTag():
			// Peel annotated tags so the SHA exposed to callers is
			// always the underlying commit hash, not the tag object.
			peeled, err := peelHashToCommit(repo, ref.Hash())
			if err != nil {
				return nil // skip unresolvable tags
			}
			out.Tags = append(out.Tags, &domain.Ref{
				Name: name.Short(),
				SHA:  peeled.String(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list refs: foreach: %w", err)
	}

	sort.Slice(out.Branches, func(i, j int) bool { return out.Branches[i].Name < out.Branches[j].Name })
	sort.Slice(out.Tags, func(i, j int) bool { return out.Tags[i].Name < out.Tags[j].Name })
	return out, nil
}

// ListCommits walks history from ref, skipping offset and returning at most
// limit commits. Returns ErrEmptyRepo for an unborn default branch.
func (g *GoGit) ListCommits(path, ref string, offset, limit int) ([]*domain.Commit, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, err
	}

	hash, err := resolveRef(repo, ref)
	if err != nil {
		// Treat "no commits yet" specially when caller passed the default branch.
		if errors.Is(err, domain.ErrRefNotFound) {
			if isEmptyRepo(repo) {
				return nil, domain.ErrEmptyRepo
			}
		}
		return nil, err
	}

	iter, err := repo.Log(&git.LogOptions{From: hash})
	if err != nil {
		return nil, fmt.Errorf("list commits: log: %w", err)
	}
	defer iter.Close()

	if offset < 0 {
		offset = 0
	}
	out := make([]*domain.Commit, 0, limit)
	skipped := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if skipped < offset {
			skipped++
			return nil
		}
		if limit > 0 && len(out) >= limit {
			return storerStop
		}
		out = append(out, toDomainCommit(c))
		return nil
	})
	if err != nil && !errors.Is(err, storerStop) {
		return nil, fmt.Errorf("list commits: walk: %w", err)
	}
	return out, nil
}

// storerStop is a sentinel used to short-circuit ForEach once limit is hit.
var storerStop = errors.New("stop")

// CommitByID loads a commit by SHA and returns it along with the diff against
// its first parent. Root commits are diffed against an empty tree so the
// initial commit's files show up as additions instead of an empty changeset.
func (g *GoGit) CommitByID(path, sha string) (*domain.CommitWithDiff, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, err
	}

	hash := plumbing.NewHash(sha)
	commit, err := repo.CommitObject(hash)
	if err != nil {
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, domain.ErrRefNotFound
		}
		return nil, fmt.Errorf("commit by id: %w", err)
	}

	out := &domain.CommitWithDiff{Commit: toDomainCommit(commit)}

	currTree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("commit by id: tree: %w", err)
	}

	var parentTree *object.Tree
	if commit.NumParents() > 0 {
		parent, err := commit.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("commit by id: parent: %w", err)
		}
		parentTree, err = parent.Tree()
		if err != nil {
			return nil, fmt.Errorf("commit by id: parent tree: %w", err)
		}
	} else {
		// Root commit: diff against an empty tree so every file in the
		// commit is reported as added. go-git's NewTreeRootNode treats a
		// nil/empty *Tree as a node with no children, which is exactly
		// what we want here.
		parentTree = &object.Tree{}
	}

	patch, err := parentTree.Patch(currTree)
	if err != nil {
		return nil, fmt.Errorf("commit by id: patch: %w", err)
	}
	out.Diff = patchToFileDiffs(patch)
	return out, nil
}

// Tree returns the immediate entries under treePath at refOrSha. An empty
// treePath returns the root tree.
func (g *GoGit) Tree(path, refOrSha, treePath string) ([]*domain.TreeEntry, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, err
	}

	hash, err := resolveRef(repo, refOrSha)
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("tree: commit: %w", err)
	}
	root, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("tree: root: %w", err)
	}

	current := root
	treePath = strings.Trim(treePath, "/")
	if treePath != "" {
		for part := range strings.SplitSeq(treePath, "/") {
			next, err := current.Tree(part)
			if err != nil {
				if errors.Is(err, object.ErrDirectoryNotFound) {
					return nil, domain.ErrPathNotFound
				}
				return nil, fmt.Errorf("tree: descend %q: %w", part, err)
			}
			current = next
		}
	}

	out := make([]*domain.TreeEntry, 0, len(current.Entries))
	for _, e := range current.Entries {
		kind := entryKind(e.Mode)
		fullPath := e.Name
		if treePath != "" {
			fullPath = treePath + "/" + e.Name
		}
		te := &domain.TreeEntry{
			Name: e.Name,
			Path: fullPath,
			Kind: kind,
			SHA:  e.Hash.String(),
		}
		if kind == domain.EntryKindBlob || kind == domain.EntryKindExecutable {
			if size, err := current.Size(e.Name); err == nil {
				te.Size = size
			}
		}
		out = append(out, te)
	}

	sort.Slice(out, func(i, j int) bool {
		ai, aj := out[i].Kind == domain.EntryKindTree, out[j].Kind == domain.EntryKindTree
		if ai != aj {
			return ai // trees first
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// TreeView returns the entries Tree() would, plus per-entry last commit
// and a top-level last_commit + total_commits for the ref. Single walk of
// the commit log: counts every commit, assigns each entry's last_commit on
// the first commit (newest first) that touches a path prefixed by the
// entry's path. Stops diff work (but not the count) once every entry has
// been assigned.
func (g *GoGit) TreeView(path, refOrSha, treePath string) (*domain.TreeView, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, err
	}
	hash, err := resolveRef(repo, refOrSha)
	if err != nil {
		// Empty repo (HEAD points at an unborn branch): return a
		// well-formed empty view rather than 404, so the file page has
		// something to render.
		if errors.Is(err, domain.ErrRefNotFound) && isEmptyRepo(repo) {
			return &domain.TreeView{Entries: []*domain.EntryWithCommit{}}, nil
		}
		return nil, err
	}

	entries, err := g.Tree(path, refOrSha, treePath)
	if err != nil {
		return nil, err
	}

	enriched := make([]*domain.EntryWithCommit, len(entries))
	for i, e := range entries {
		enriched[i] = &domain.EntryWithCommit{TreeEntry: e}
	}
	unassigned := len(enriched)

	iter, err := repo.Log(&git.LogOptions{From: hash})
	if err != nil {
		return nil, fmt.Errorf("tree view: log: %w", err)
	}
	defer iter.Close()

	var lastCommit *domain.Commit
	var total int64

	err = iter.ForEach(func(c *object.Commit) error {
		total++
		if lastCommit == nil {
			lastCommit = toDomainCommit(c)
		}
		if unassigned == 0 {
			// Already found every entry's last commit — keep walking
			// (we still need to count) but skip the expensive diff.
			return nil
		}
		changed, err := commitChangedPaths(c)
		if err != nil {
			return err
		}
		dc := toDomainCommit(c)
		for _, entry := range enriched {
			if entry.LastCommit != nil {
				continue
			}
			if entryTouchedByPaths(entry, changed) {
				entry.LastCommit = dc
				unassigned--
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("tree view: walk: %w", err)
	}

	return &domain.TreeView{
		Entries:      enriched,
		LastCommit:   lastCommit,
		TotalCommits: total,
	}, nil
}

// entryTouchedByPaths reports whether any path in changed is the entry's
// own path (for a blob) or any descendant of the entry's path (for a tree).
// Symlinks/executables are treated as blobs for this check.
func entryTouchedByPaths(e *domain.EntryWithCommit, changed map[string]struct{}) bool {
	if _, ok := changed[e.Path]; ok {
		return true
	}
	if e.Kind == domain.EntryKindTree {
		prefix := e.Path + "/"
		for p := range changed {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		}
	}
	return false
}

// commitChangedPaths returns the set of file paths changed in commit c
// against its first parent. Root commits report every file in the tree.
// Both the old and new paths are reported for renames so the caller's
// prefix match works either way.
func commitChangedPaths(c *object.Commit) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if c.NumParents() == 0 {
		tree, err := c.Tree()
		if err != nil {
			return nil, err
		}
		err = tree.Files().ForEach(func(f *object.File) error {
			out[f.Name] = struct{}{}
			return nil
		})
		return out, err
	}
	parent, err := c.Parent(0)
	if err != nil {
		return nil, err
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return nil, err
	}
	currTree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	changes, err := parentTree.Diff(currTree)
	if err != nil {
		return nil, err
	}
	for _, ch := range changes {
		if ch.From.Name != "" {
			out[ch.From.Name] = struct{}{}
		}
		if ch.To.Name != "" {
			out[ch.To.Name] = struct{}{}
		}
	}
	return out, nil
}

// Blob returns the raw bytes of the file at refOrSha:filePath. The binary
// flag is true iff any of the first 8 KiB contains a NUL byte.
func (g *GoGit) Blob(path, refOrSha, filePath string) ([]byte, bool, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, false, err
	}
	hash, err := resolveRef(repo, refOrSha)
	if err != nil {
		return nil, false, err
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, false, fmt.Errorf("blob: commit: %w", err)
	}
	root, err := commit.Tree()
	if err != nil {
		return nil, false, fmt.Errorf("blob: root tree: %w", err)
	}

	filePath = strings.Trim(filePath, "/")
	entry, err := root.FindEntry(filePath)
	if err != nil {
		if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, object.ErrFileNotFound) {
			return nil, false, domain.ErrPathNotFound
		}
		return nil, false, fmt.Errorf("blob: find: %w", err)
	}
	if entry.Mode == filemode.Dir || entry.Mode == filemode.Submodule {
		return nil, false, domain.ErrNotABlob
	}

	blob, err := repo.BlobObject(entry.Hash)
	if err != nil {
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, false, domain.ErrPathNotFound
		}
		return nil, false, fmt.Errorf("blob: object: %w", err)
	}
	rd, err := blob.Reader()
	if err != nil {
		return nil, false, fmt.Errorf("blob: reader: %w", err)
	}
	defer rd.Close()
	content, err := io.ReadAll(rd)
	if err != nil {
		return nil, false, fmt.Errorf("blob: read: %w", err)
	}

	return content, isBinary(content), nil
}

// CreateBranch creates a new branch ref pointing at startRef. The branch must
// not already exist; the name must satisfy go-git's ref-name rules plus our
// own conservative pre-checks.
func (g *GoGit) CreateBranch(path, branchName, startRef string) error {
	repo, err := openRepo(path)
	if err != nil {
		return err
	}
	if !domain.IsValidRefName(branchName) {
		return domain.ErrInvalidRefName
	}
	refName := plumbing.NewBranchReferenceName(branchName)
	if !refName.IsBranch() {
		return domain.ErrInvalidRefName
	}
	if err := refName.Validate(); err != nil {
		return domain.ErrInvalidRefName
	}

	if _, err := repo.Reference(refName, false); err == nil {
		return domain.ErrBranchExists
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("create branch: check existing: %w", err)
	}

	hash, err := resolveRef(repo, startRef)
	if err != nil {
		return err
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
		return fmt.Errorf("create branch: set ref: %w", err)
	}
	return nil
}

// CreateBranchAt points branchName directly at commitSHA without resolving
// a ref name first — the caller already holds a validated commit SHA.
func (g *GoGit) CreateBranchAt(path, branchName, commitSHA string) error {
	repo, err := openRepo(path)
	if err != nil {
		return err
	}
	if !domain.IsValidRefName(branchName) {
		return domain.ErrInvalidRefName
	}
	refName := plumbing.NewBranchReferenceName(branchName)
	if !refName.IsBranch() {
		return domain.ErrInvalidRefName
	}
	if err := refName.Validate(); err != nil {
		return domain.ErrInvalidRefName
	}

	if _, err := repo.Reference(refName, false); err == nil {
		return domain.ErrBranchExists
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("create branch at: check existing: %w", err)
	}

	hash := plumbing.NewHash(commitSHA)
	if _, err := repo.CommitObject(hash); err != nil {
		return domain.ErrRefNotFound
	}

	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
		return fmt.Errorf("create branch at: set ref: %w", err)
	}
	return nil
}

// DeleteBranch removes the named branch. Refuses to delete the branch that
// HEAD currently points at — caller must SetHEAD elsewhere first.
func (g *GoGit) DeleteBranch(path, branchName string) error {
	repo, err := openRepo(path)
	if err != nil {
		return err
	}
	refName := plumbing.NewBranchReferenceName(branchName)
	if _, err := repo.Reference(refName, false); err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return domain.ErrRefNotFound
		}
		return fmt.Errorf("delete branch: lookup: %w", err)
	}

	headRef, err := repo.Reference(plumbing.HEAD, false)
	if err == nil && headRef.Type() == plumbing.SymbolicReference {
		if headRef.Target() == refName {
			return domain.ErrCannotDeleteHEAD
		}
	}

	if err := repo.Storer.RemoveReference(refName); err != nil {
		return fmt.Errorf("delete branch: remove: %w", err)
	}
	return nil
}

// SetHEAD updates the symbolic HEAD ref to point at refs/heads/branchName.
// The branch must already exist.
func (g *GoGit) SetHEAD(path, branchName string) error {
	repo, err := openRepo(path)
	if err != nil {
		return err
	}
	refName := plumbing.NewBranchReferenceName(branchName)
	if _, err := repo.Reference(refName, false); err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return domain.ErrRefNotFound
		}
		return fmt.Errorf("set head: lookup branch: %w", err)
	}
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, refName)
	if err := repo.Storer.SetReference(headRef); err != nil {
		return fmt.Errorf("set head: %w", err)
	}
	return nil
}

// CreateLightweightTag creates a tag ref that points directly at the commit
// resolved from refOrSha. No tag object is written.
func (g *GoGit) CreateLightweightTag(path, tagName, refOrSha string) error {
	repo, hash, err := prepareNewTag(path, tagName, refOrSha)
	if err != nil {
		return err
	}
	refName := plumbing.NewTagReferenceName(tagName)
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
		return fmt.Errorf("create lightweight tag: set ref: %w", err)
	}
	return nil
}

// CreateAnnotatedTag writes a tag object with the given message and tagger
// identity, then points tagName at it. The target must resolve to a commit.
func (g *GoGit) CreateAnnotatedTag(path, tagName, refOrSha, message string, tagger domain.Signature) error {
	repo, hash, err := prepareNewTag(path, tagName, refOrSha)
	if err != nil {
		return err
	}

	// Confirm the target is a commit; annotated tags in M3 only point at commits.
	if _, err := repo.CommitObject(hash); err != nil {
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return domain.ErrRefNotFound
		}
		return fmt.Errorf("create annotated tag: target commit: %w", err)
	}

	when := tagger.When
	if when.IsZero() {
		when = time.Now()
	}
	tag := &object.Tag{
		Name:       tagName,
		Tagger:     object.Signature{Name: tagger.Name, Email: tagger.Email, When: when},
		Message:    message,
		TargetType: plumbing.CommitObject,
		Target:     hash,
	}
	st := repo.Storer
	obj := st.NewEncodedObject()
	if err := tag.Encode(obj); err != nil {
		return fmt.Errorf("create annotated tag: encode: %w", err)
	}
	tagHash, err := st.SetEncodedObject(obj)
	if err != nil {
		return fmt.Errorf("create annotated tag: store object: %w", err)
	}
	refName := plumbing.NewTagReferenceName(tagName)
	if err := st.SetReference(plumbing.NewHashReference(refName, tagHash)); err != nil {
		return fmt.Errorf("create annotated tag: set ref: %w", err)
	}
	return nil
}

// DeleteTag removes the tag ref. Tag objects (for annotated tags) are left
// orphaned for git gc to reap.
func (g *GoGit) DeleteTag(path, tagName string) error {
	repo, err := openRepo(path)
	if err != nil {
		return err
	}
	refName := plumbing.NewTagReferenceName(tagName)
	if _, err := repo.Reference(refName, false); err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return domain.ErrRefNotFound
		}
		return fmt.Errorf("delete tag: lookup: %w", err)
	}
	if err := repo.Storer.RemoveReference(refName); err != nil {
		return fmt.Errorf("delete tag: remove: %w", err)
	}
	return nil
}

// prepareNewTag opens the repo, validates a fresh tag name, and resolves the
// target ref to a hash. Returned for use by both lightweight and annotated tag
// constructors.
func prepareNewTag(path, tagName, refOrSha string) (*git.Repository, plumbing.Hash, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}
	if !domain.IsValidRefName(tagName) {
		return nil, plumbing.ZeroHash, domain.ErrInvalidRefName
	}
	refName := plumbing.NewTagReferenceName(tagName)
	if !refName.IsTag() {
		return nil, plumbing.ZeroHash, domain.ErrInvalidRefName
	}
	if err := refName.Validate(); err != nil {
		return nil, plumbing.ZeroHash, domain.ErrInvalidRefName
	}
	if _, err := repo.Reference(refName, false); err == nil {
		return nil, plumbing.ZeroHash, domain.ErrTagExists
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, plumbing.ZeroHash, fmt.Errorf("prepare tag: check existing: %w", err)
	}
	hash, err := resolveRef(repo, refOrSha)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}
	return repo, hash, nil
}

// ContainsCommit returns every branch / tag whose tip is descended from
// (or equal to) the supplied commit. Lightweight tags pointing directly at
// a commit and annotated tags whose target commit matches are both included.
// O(refs) MergeBase calls — fine for M3 scale; if/when ref counts explode
// we can swap in a commit-graph traversal.
func (g *GoGit) ContainsCommit(path, sha string) (*domain.ContainingRefs, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, err
	}
	targetHash, err := resolveRef(repo, sha)
	if err != nil {
		return nil, err
	}
	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, domain.ErrRefNotFound
	}

	out := &domain.ContainingRefs{
		Branches: []*domain.Ref{},
		Tags:     []*domain.Ref{},
	}

	iter, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("contains: refs: %w", err)
	}
	defer iter.Close()

	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.SymbolicReference {
			return nil
		}
		name := ref.Name()
		if !name.IsBranch() && !name.IsTag() {
			return nil
		}
		// Peel annotated tags down to a commit; lightweight tags and
		// branches already point at one. Anything that doesn't peel to a
		// commit (e.g. a tag-of-tag, very rare) we just skip.
		tipHash, err := peelHashToCommit(repo, ref.Hash())
		if err != nil {
			return nil
		}
		tipCommit, err := repo.CommitObject(tipHash)
		if err != nil {
			return nil
		}
		isAncestor, err := targetCommit.IsAncestor(tipCommit)
		if err != nil {
			return nil
		}
		if !isAncestor {
			return nil
		}
		entry := &domain.Ref{Name: name.Short(), SHA: tipHash.String()}
		if name.IsBranch() {
			out.Branches = append(out.Branches, entry)
		} else {
			out.Tags = append(out.Tags, entry)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("contains: foreach: %w", err)
	}

	sort.Slice(out.Branches, func(i, j int) bool { return out.Branches[i].Name < out.Branches[j].Name })
	sort.Slice(out.Tags, func(i, j int) bool { return out.Tags[i].Name < out.Tags[j].Name })
	return out, nil
}

// IsAncestor checks whether ancestor is reachable from descendant. Both
// inputs are resolved through the standard ref/SHA fallback chain.
func (g *GoGit) IsAncestor(path, ancestor, descendant string) (bool, error) {
	repo, err := openRepo(path)
	if err != nil {
		return false, err
	}
	aHash, err := resolveRef(repo, ancestor)
	if err != nil {
		return false, err
	}
	dHash, err := resolveRef(repo, descendant)
	if err != nil {
		return false, err
	}
	aCommit, err := repo.CommitObject(aHash)
	if err != nil {
		return false, domain.ErrRefNotFound
	}
	dCommit, err := repo.CommitObject(dHash)
	if err != nil {
		return false, domain.ErrRefNotFound
	}
	return aCommit.IsAncestor(dCommit)
}

// ResolveCommit returns the commit SHA the ref resolves to. Unborn branches
// (HEAD pointing at an unborn ref, or an explicit branch name that hasn't been
// created yet for a fresh repo) yield an empty string with no error so callers
// can disambiguate "doesn't exist anywhere" (ErrRefNotFound) from "branch
// doesn't have a commit yet" (empty).
func (g *GoGit) ResolveCommit(path, ref string) (string, error) {
	repo, err := openRepo(path)
	if err != nil {
		return "", err
	}
	hash, err := resolveRef(repo, ref)
	if err != nil {
		if errors.Is(err, domain.ErrRefNotFound) && isEmptyRepo(repo) {
			return "", nil
		}
		return "", err
	}
	return hash.String(), nil
}

// MergeBranch merges fromRef into intoBranch on the bare repo at path. See
// the domain interface comment for the supported modes. The implementation
// uses go-git's merge-base + the in-memory three-way merge over the trees,
// then writes a new commit object and updates the branch ref atomically.
//
// Conflicts surface as domain.ErrMergeConflict; callers should report a
// user-facing 409 rather than treating this as an unexpected error.
func (g *GoGit) MergeBranch(path, intoBranch, fromRef, message string, author domain.Signature) (string, string, error) {
	repo, err := openRepo(path)
	if err != nil {
		return "", "", err
	}

	fromHash, err := resolveRef(repo, fromRef)
	if err != nil {
		return "", "", err
	}
	fromCommit, err := repo.CommitObject(fromHash)
	if err != nil {
		return "", "", fmt.Errorf("merge: from commit: %w", err)
	}

	intoRefName := plumbing.NewBranchReferenceName(intoBranch)

	// Case 1: unborn base — just point the branch at fromRef.
	intoRef, err := repo.Reference(intoRefName, false)
	if err != nil {
		if !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return "", "", fmt.Errorf("merge: lookup base: %w", err)
		}
		if err := repo.Storer.SetReference(plumbing.NewHashReference(intoRefName, fromHash)); err != nil {
			return "", "", fmt.Errorf("merge: set base: %w", err)
		}
		return fromHash.String(), "fast-forward", nil
	}

	intoHash := intoRef.Hash()
	intoCommit, err := repo.CommitObject(intoHash)
	if err != nil {
		return "", "", fmt.Errorf("merge: into commit: %w", err)
	}

	// Case 2: already at the same commit.
	if intoHash == fromHash {
		return intoHash.String(), "up-to-date", nil
	}

	// Case 3: from is an ancestor of into → nothing to do.
	fromIsAncestor, err := fromCommit.IsAncestor(intoCommit)
	if err != nil {
		return "", "", fmt.Errorf("merge: ancestor check (from): %w", err)
	}
	if fromIsAncestor {
		return intoHash.String(), "up-to-date", nil
	}

	// Case 4: into is an ancestor of from → fast-forward.
	intoIsAncestor, err := intoCommit.IsAncestor(fromCommit)
	if err != nil {
		return "", "", fmt.Errorf("merge: ancestor check (into): %w", err)
	}
	if intoIsAncestor {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(intoRefName, fromHash)); err != nil {
			return "", "", fmt.Errorf("merge: ff set: %w", err)
		}
		return fromHash.String(), "fast-forward", nil
	}

	// Case 5: merge-commit — create a single merge commit with parents
	// (intoBranch, fromRef). `git merge-tree` runs a real line-level
	// three-way merge: it picks the merge base(s) itself, auto-resolves
	// non-overlapping edits to the same file, and writes the merged tree
	// into the object store. We then hang a new merge commit off that tree.
	// Only genuine overlapping-hunk conflicts surface as ErrMergeConflict.
	mergedTreeHash, err := mergeTreeCLI(path, intoHash.String(), fromHash.String())
	if err != nil {
		if errors.Is(err, domain.ErrMergeConflict) {
			return "", "", err
		}
		return "", "", fmt.Errorf("merge: %w", err)
	}

	when := author.When
	if when.IsZero() {
		when = time.Now()
	}
	sig := object.Signature{Name: author.Name, Email: author.Email, When: when}

	msg := message
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	mergeCommit := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      msg,
		TreeHash:     mergedTreeHash,
		ParentHashes: []plumbing.Hash{intoHash, fromHash},
	}
	obj := repo.Storer.NewEncodedObject()
	if err := mergeCommit.Encode(obj); err != nil {
		return "", "", fmt.Errorf("merge: encode merge commit: %w", err)
	}
	mergeHash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", "", fmt.Errorf("merge: store merge commit: %w", err)
	}

	// Update only the base branch ref — the issue branch ref stays
	// unchanged, preserving the original history.
	if err := repo.Storer.SetReference(plumbing.NewHashReference(intoRefName, mergeHash)); err != nil {
		return "", "", fmt.Errorf("merge: set base ref: %w", err)
	}
	return mergeHash.String(), "merge-commit", nil
}

// mergeTreeCLI performs a real three-way merge of fromHash into intoHash via
// `git merge-tree --write-tree`. Unlike a file-granular blob compare, it
// resolves non-overlapping edits to the same file automatically and reports
// only genuine overlapping-hunk conflicts (the M4 "rebase first" workflow no
// longer applies to merely co-touched files). It finds the merge base(s)
// itself — recursively merging criss-cross bases like git's ort strategy —
// and writes the merged tree into the bare repo's object store, returning that
// tree's hash for the caller to build a merge commit on.
//
// Exit status maps to: 0 → clean merge (stdout is the tree OID); 1 → conflicts
// (domain.ErrMergeConflict, with the conflicting paths wrapped into the
// message); anything else → a merge we can't complete (e.g. unrelated
// histories), surfaced as a plain error.
func mergeTreeCLI(path, intoHash, fromHash string) (plumbing.Hash, error) {
	cmd := exec.Command("git", "merge-tree", "--write-tree", "--name-only",
		intoHash, fromHash)
	cmd.Dir = path
	cmd.Env = append(os.Environ(), "GIT_DIR="+path)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	lines := strings.Split(strings.Trim(stdout.String(), "\n"), "\n")

	if err == nil {
		// Clean merge: the first stdout line is the merged tree OID.
		if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
			return plumbing.ZeroHash, fmt.Errorf("merge-tree: empty output")
		}
		return plumbing.NewHash(strings.TrimSpace(lines[0])), nil
	}

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return plumbing.ZeroHash, fmt.Errorf("merge-tree: %w", err)
	}
	if ee.ExitCode() == 1 {
		// Conflicted: with --name-only the lines after the OID are the
		// conflicting paths, terminated by a blank line.
		var files []string
		for _, l := range lines[1:] {
			if strings.TrimSpace(l) == "" {
				break
			}
			files = append(files, l)
		}
		if len(files) == 0 {
			return plumbing.ZeroHash, domain.ErrMergeConflict
		}
		return plumbing.ZeroHash, fmt.Errorf("%w: conflicting paths: %s",
			domain.ErrMergeConflict, strings.Join(files, ", "))
	}

	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = strings.TrimSpace(stdout.String())
	}
	return plumbing.ZeroHash, fmt.Errorf("merge-tree (exit %d): %s", ee.ExitCode(), msg)
}

// DiffMergeBase computes the diff from the merge-base of base and topic to
// topic itself — the equivalent of `git diff base...topic`. When the merge-base
// cannot be determined (no common history, empty repo), the result is empty.
func (g *GoGit) DiffMergeBase(path, base, topic string) ([]*domain.FileDiff, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, err
	}
	baseHash, err := resolveRef(repo, base)
	if err != nil {
		return nil, err
	}
	topicHash, err := resolveRef(repo, topic)
	if err != nil {
		return nil, err
	}
	baseCommit, err := repo.CommitObject(baseHash)
	if err != nil {
		return nil, fmt.Errorf("diff merge-base: base commit: %w", err)
	}
	topicCommit, err := repo.CommitObject(topicHash)
	if err != nil {
		return nil, fmt.Errorf("diff merge-base: topic commit: %w", err)
	}

	// MergeBase returns a slice — the first is the best common ancestor.
	bases, err := baseCommit.MergeBase(topicCommit)
	if err != nil || len(bases) == 0 {
		// No common history → no diff to show.
		return []*domain.FileDiff{}, nil
	}
	patch, err := bases[0].Patch(topicCommit)
	if err != nil {
		return nil, fmt.Errorf("diff merge-base: patch: %w", err)
	}
	return patchToFileDiffs(patch), nil
}

// DiffRefs computes the changes going from from to to. Either side may be
// a branch, tag, or SHA.
func (g *GoGit) DiffRefs(path, from, to string) ([]*domain.FileDiff, error) {
	repo, err := openRepo(path)
	if err != nil {
		return nil, err
	}
	fromHash, err := resolveRef(repo, from)
	if err != nil {
		return nil, err
	}
	toHash, err := resolveRef(repo, to)
	if err != nil {
		return nil, err
	}
	fromCommit, err := repo.CommitObject(fromHash)
	if err != nil {
		return nil, fmt.Errorf("diff refs: from commit: %w", err)
	}
	toCommit, err := repo.CommitObject(toHash)
	if err != nil {
		return nil, fmt.Errorf("diff refs: to commit: %w", err)
	}
	patch, err := fromCommit.Patch(toCommit)
	if err != nil {
		return nil, fmt.Errorf("diff refs: patch: %w", err)
	}
	return patchToFileDiffs(patch), nil
}

// --- helpers ---

// openRepo wraps git.PlainOpen, mapping the "no repo" sentinel to the domain
// error so callers can branch on it without importing go-git.
func openRepo(path string) (*git.Repository, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, domain.ErrRepoNotFound
		}
		return nil, fmt.Errorf("open repo: %w", err)
	}
	return repo, nil
}

// resolveRef tries the given ref as a branch, then a tag, then as a raw
// revision (SHA or anything ResolveRevision accepts). Returns ErrRefNotFound
// if none match.
// resolveRef looks up a ref (branch / tag / SHA / abbrev) and returns the
// hash of the **commit it points at**. Annotated tags are transparently
// peeled to their underlying commit — every internal caller wants a commit
// hash, so doing the peel here keeps the rest of the impl uniform.
func resolveRef(repo *git.Repository, ref string) (plumbing.Hash, error) {
	if ref == "" {
		return plumbing.ZeroHash, domain.ErrRefNotFound
	}

	hash, err := lookupRefHash(repo, ref)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return peelHashToCommit(repo, hash)
}

func lookupRefHash(repo *git.Repository, ref string) (plumbing.Hash, error) {
	if r, err := repo.Reference(plumbing.NewBranchReferenceName(ref), true); err == nil {
		return r.Hash(), nil
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, fmt.Errorf("resolve ref: branch lookup: %w", err)
	}

	if r, err := repo.Reference(plumbing.NewTagReferenceName(ref), true); err == nil {
		return r.Hash(), nil
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, fmt.Errorf("resolve ref: tag lookup: %w", err)
	}

	h, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return plumbing.ZeroHash, domain.ErrRefNotFound
	}
	return *h, nil
}

// peelHashToCommit returns the input hash unchanged if it already points at
// a commit (the common case: branch, lightweight tag, bare SHA), and
// follows the chain through a *object.Tag for annotated tags. Returns
// ErrRefNotFound if the hash points at something that has no commit
// (e.g. a blob).
func peelHashToCommit(repo *git.Repository, hash plumbing.Hash) (plumbing.Hash, error) {
	obj, err := repo.Object(plumbing.AnyObject, hash)
	if err != nil {
		return plumbing.ZeroHash, domain.ErrRefNotFound
	}
	switch v := obj.(type) {
	case *object.Commit:
		return v.Hash, nil
	case *object.Tag:
		commit, err := v.Commit()
		if err != nil {
			return plumbing.ZeroHash, domain.ErrRefNotFound
		}
		return commit.Hash, nil
	default:
		return plumbing.ZeroHash, domain.ErrRefNotFound
	}
}

// isEmptyRepo reports whether the repo has no commit-bearing branch.
func isEmptyRepo(repo *git.Repository) bool {
	iter, err := repo.References()
	if err != nil {
		return false
	}
	defer iter.Close()
	hasBranch := false
	_ = iter.ForEach(func(r *plumbing.Reference) error {
		if r.Type() == plumbing.SymbolicReference {
			return nil
		}
		if r.Name().IsBranch() {
			hasBranch = true
		}
		return nil
	})
	return !hasBranch
}

func toDomainCommit(c *object.Commit) *domain.Commit {
	parents := make([]string, 0, len(c.ParentHashes))
	for _, p := range c.ParentHashes {
		parents = append(parents, p.String())
	}
	return &domain.Commit{
		SHA:        c.Hash.String(),
		ParentSHAs: parents,
		Author: domain.Signature{
			Name:  c.Author.Name,
			Email: c.Author.Email,
			When:  c.Author.When,
		},
		Committer: domain.Signature{
			Name:  c.Committer.Name,
			Email: c.Committer.Email,
			When:  c.Committer.When,
		},
		Message:     c.Message,
		CommittedAt: c.Committer.When,
	}
}

func entryKind(mode filemode.FileMode) string {
	switch mode {
	case filemode.Submodule:
		return domain.EntryKindSubmodule
	case filemode.Symlink:
		return domain.EntryKindSymlink
	case filemode.Dir:
		return domain.EntryKindTree
	case filemode.Executable:
		return domain.EntryKindExecutable
	default:
		return domain.EntryKindBlob
	}
}

func isBinary(content []byte) bool {
	limit := min(len(content), 8*1024)
	return bytes.IndexByte(content[:limit], 0x00) >= 0
}

// singleFilePatch adapts one fdiff.FilePatch to fdiff.Patch so it can be
// rendered standalone via the unified encoder. This is the cleanest way to
// emit per-file unified diff text in go-git v5: the public Patch.String()
// concatenates every file, with no per-FilePatch accessor.
type singleFilePatch struct {
	fp fdiff.FilePatch
}

func (s *singleFilePatch) FilePatches() []fdiff.FilePatch { return []fdiff.FilePatch{s.fp} }
func (s *singleFilePatch) Message() string                { return "" }

// patchToFileDiffs maps go-git's per-file patches into domain.FileDiff. Status
// is derived from the presence of from/to File:
//   - from nil          -> added
//   - to nil            -> deleted
//   - from.Path != to.Path -> renamed
//   - otherwise         -> modified
//
// Patch text is rendered per-file via singleFilePatch + UnifiedEncoder so each
// FileDiff carries only its own hunks. Binary patches carry no chunks (go-git
// represents them with IsBinary()=true) and we emit an empty patch string.
func patchToFileDiffs(p *object.Patch) []*domain.FileDiff {
	if p == nil {
		return nil
	}
	patches := p.FilePatches()
	out := make([]*domain.FileDiff, 0, len(patches))
	for _, fp := range patches {
		from, to := fp.Files()
		fd := &domain.FileDiff{Binary: fp.IsBinary()}

		switch {
		case from == nil && to != nil:
			fd.NewPath = to.Path()
			fd.Status = domain.DiffStatusAdded
		case to == nil && from != nil:
			fd.OldPath = from.Path()
			fd.Status = domain.DiffStatusDeleted
		case from != nil && to != nil:
			fd.OldPath = from.Path()
			fd.NewPath = to.Path()
			if from.Path() != to.Path() {
				fd.Status = domain.DiffStatusRenamed
			} else {
				fd.Status = domain.DiffStatusModified
			}
		default:
			// Both nil: skip degenerate entry.
			continue
		}

		if !fd.Binary {
			var buf bytes.Buffer
			enc := fdiff.NewUnifiedEncoder(&buf, fdiff.DefaultContextLines)
			if err := enc.Encode(&singleFilePatch{fp: fp}); err == nil {
				fd.Patch = buf.String()
			}
		}

		out = append(out, fd)
	}
	return out
}

// CheckFastForward reports whether head-ref is a descendant of base-ref
// (i.e. fast-forward is possible). Uses IsAncestor internally; the
// value-add is the structured (bool, mode) return and the zero-headRef /
// unresolved-ref guard rails.
func (g *GoGit) CheckFastForward(path, baseRef, headRef string) (bool, string, error) {
	if headRef == "" {
		return false, "unknown", nil
	}
	ok, err := g.IsAncestor(path, baseRef, headRef)
	if err != nil {
		if errors.Is(err, domain.ErrRefNotFound) {
			return false, "unknown", nil
		}
		return false, "unknown", err
	}
	if ok {
		return true, "fast-forward", nil
	}
	return false, "diverged", nil
}

// CheckAutoMerge evaluates whether headRef can be merged into baseRef
// using a merge-commit strategy without modifying refs. It is the shared
// pre-flight check for issue_mergeable and issue_merge.
func (g *GoGit) CheckAutoMerge(path, baseRef, headRef string) (bool, string, string, error) {
	if headRef == "" {
		return false, "unknown", "issue branch has no commits yet", nil
	}
	ok, err := g.IsAncestor(path, baseRef, headRef)
	if err != nil {
		if errors.Is(err, domain.ErrRefNotFound) {
			return false, "unknown", "cannot resolve one or both refs", nil
		}
		return false, "unknown", err.Error(), err
	}
	if ok {
		return true, "fast-forward", "", nil
	}
	// up-to-date: head already reachable from base
	ok, err = g.IsAncestor(path, headRef, baseRef)
	if err != nil {
		if errors.Is(err, domain.ErrRefNotFound) {
			return false, "unknown", "cannot resolve one or both refs", nil
		}
		return false, "unknown", err.Error(), err
	}
	if ok {
		return true, "up-to-date", "issue branch is already included in base", nil
	}

	// Diverged — try three-way merge (same logic as MergeBranch's
	// merge-commit path) so issue_mergeable's conflict detection is
	// exactly equivalent to what issue_merge will encounter.
	repo, err := openRepo(path)
	if err != nil {
		return false, "unknown", err.Error(), err
	}
	baseHash, err := resolveRef(repo, baseRef)
	if err != nil {
		return false, "unknown", "cannot resolve base ref", nil
	}
	headHash, err := resolveRef(repo, headRef)
	if err != nil {
		return false, "unknown", "cannot resolve head ref", nil
	}
	if _, err := mergeTreeCLI(path, baseHash.String(), headHash.String()); err != nil {
		if errors.Is(err, domain.ErrMergeConflict) {
			return false, "conflicted", "merge would conflict — resolve conflicts manually", nil
		}
		return false, "unknown", err.Error(), nil
	}
	return true, "merge-commit", "will create a merge commit on " + baseRef, nil
}

// ApplyPatch applies a unified diff patchText onto branch at the bare
// repo path, creating a new commit. Uses git apply --cached with a
// temporary index so the bare repo doesn't need a worktree.
// The author identifies the original patch submitter; the committer
// identifies who is applying it.
func (g *GoGit) ApplyPatch(path, branch, patchText, message string, author, committer domain.Signature) (string, error) {
	if patchText == "" {
		return "", fmt.Errorf("apply patch: empty patch text")
	}
	if branch == "" {
		return "", fmt.Errorf("apply patch: empty branch")
	}

	repo, err := openRepo(path)
	if err != nil {
		return "", fmt.Errorf("apply patch: %w", err)
	}

	branchRef := plumbing.NewBranchReferenceName(branch)
	baseRef, err := repo.Reference(branchRef, false)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return "", fmt.Errorf("apply patch: branch %q not found", branch)
		}
		return "", fmt.Errorf("apply patch: resolve branch: %w", err)
	}
	baseHash := baseRef.Hash()

	// Create a temporary index file for the apply.
	tmpIndex, err := os.CreateTemp("", "hangrix-git-index-")
	if err != nil {
		return "", fmt.Errorf("apply patch: create tmp index: %w", err)
	}
	indexPath := tmpIndex.Name()
	tmpIndex.Close()
	defer os.Remove(indexPath)

	// Read the base tree into the temporary index.
	readTreeCmd := exec.Command("git", "read-tree", baseHash.String())
	readTreeCmd.Dir = path
	readTreeCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexPath, "GIT_DIR="+path)
	if out, err := readTreeCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("apply patch: read-tree: %w\n%s", err, string(out))
	}

	// Apply the patch to the temporary index.
	applyCmd := exec.Command("git", "apply", "--cached")
	applyCmd.Dir = path
	applyCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexPath, "GIT_DIR="+path)
	applyCmd.Stdin = strings.NewReader(patchText)
	if out, err := applyCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("apply patch: git apply --cached: %w\n%s", err, string(out))
	}

	// Write the resulting tree.
	writeTreeCmd := exec.Command("git", "write-tree")
	writeTreeCmd.Dir = path
	writeTreeCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexPath, "GIT_DIR="+path)
	treeOut, err := writeTreeCmd.Output()
	if err != nil {
		return "", fmt.Errorf("apply patch: write-tree: %w", err)
	}
	treeHash := strings.TrimSpace(string(treeOut))

	// Create the commit with commit-tree (reads message from stdin).
	now := time.Now()
	if author.When.IsZero() {
		author.When = now
	}
	if committer.When.IsZero() {
		committer.When = now
	}

	commitCmd := exec.Command("git", "commit-tree", treeHash,
		"-p", baseHash.String(),
	)
	commitCmd.Dir = path
	commitCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+author.Name,
		"GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_AUTHOR_DATE="+author.When.Format(time.RFC3339),
		"GIT_COMMITTER_NAME="+committer.Name,
		"GIT_COMMITTER_EMAIL="+committer.Email,
		"GIT_COMMITTER_DATE="+committer.When.Format(time.RFC3339),
		"GIT_DIR="+path,
	)
	commitCmd.Stdin = strings.NewReader(message)
	commitOut, err := commitCmd.Output()
	if err != nil {
		return "", fmt.Errorf("apply patch: commit-tree: %w", err)
	}
	commitHash := strings.TrimSpace(string(commitOut))

	// Update the branch ref.
	parsedHash := plumbing.NewHash(commitHash)
	if err := repo.Storer.SetReference(plumbing.NewHashReference(branchRef, parsedHash)); err != nil {
		return "", fmt.Errorf("apply patch: set ref: %w", err)
	}

	return commitHash, nil
}

// EditAndCommit replaces a single blob at filePath in the HEAD commit of
// branch, builds a new tree, creates a commit, and advances the branch ref
// using an atomic compare-and-swap against baseCommitSHA.
func (g *GoGit) EditAndCommit(path, branch, baseCommitSHA, filePath string, newContent []byte, message string, author, committer domain.Signature) (string, error) {
	repo, err := openRepo(path)
	if err != nil {
		return "", err
	}
	st := repo.Storer

	// Resolve the branch to its HEAD commit and verify it matches baseCommitSHA.
	branchRef := plumbing.NewBranchReferenceName(branch)
	ref, err := repo.Reference(branchRef, false)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return "", domain.ErrRefNotFound
		}
		return "", fmt.Errorf("edit: resolve branch: %w", err)
	}
	baseHash, err := peelHashToCommit(repo, ref.Hash())
	if err != nil {
		return "", fmt.Errorf("edit: peel branch: %w", err)
	}
	if baseHash.String() != baseCommitSHA {
		return "", domain.ErrRefChanged
	}

	baseCommit, err := repo.CommitObject(baseHash)
	if err != nil {
		return "", fmt.Errorf("edit: commit object: %w", err)
	}

	// Get the root tree and find the target entry.
	rootTree, err := baseCommit.Tree()
	if err != nil {
		return "", fmt.Errorf("edit: root tree: %w", err)
	}

	filePath = strings.Trim(filePath, "/")
	if filePath == "" {
		return "", domain.ErrPathNotFound
	}
	entry, err := rootTree.FindEntry(filePath)
	if err != nil {
		if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, object.ErrFileNotFound) {
			return "", domain.ErrPathNotFound
		}
		return "", fmt.Errorf("edit: find entry: %w", err)
	}
	if entry.Mode == filemode.Dir || entry.Mode == filemode.Submodule {
		return "", domain.ErrNotABlob
	}

	// Write the new blob.
	blobObj := st.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(int64(len(newContent)))
	w, err := blobObj.Writer()
	if err != nil {
		return "", fmt.Errorf("edit: blob writer: %w", err)
	}
	if _, err := w.Write(newContent); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("edit: write blob: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("edit: close blob: %w", err)
	}
	newBlobHash, err := st.SetEncodedObject(blobObj)
	if err != nil {
		return "", fmt.Errorf("edit: store blob: %w", err)
	}

	// Build a new root tree by replacing the target entry.
	segments := strings.Split(filePath, "/")
	newRootHash, err := replaceTreeEntry(repo, rootTree, segments, object.TreeEntry{
		Name: segments[len(segments)-1],
		Mode: entry.Mode,
		Hash: newBlobHash,
	})
	if err != nil {
		return "", fmt.Errorf("edit: replace tree: %w", err)
	}

	// Create the commit.
	now := time.Now()
	authSig := object.Signature{Name: author.Name, Email: author.Email, When: author.When}
	if authSig.When.IsZero() {
		authSig.When = now
	}
	commSig := object.Signature{Name: committer.Name, Email: committer.Email, When: committer.When}
	if commSig.When.IsZero() {
		commSig.When = now
	}
	commit := &object.Commit{
		Author:       authSig,
		Committer:    commSig,
		Message:      message,
		TreeHash:     newRootHash,
		ParentHashes: []plumbing.Hash{baseHash},
	}
	commitObj := st.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		return "", fmt.Errorf("edit: encode commit: %w", err)
	}
	newCommitHash, err := st.SetEncodedObject(commitObj)
	if err != nil {
		return "", fmt.Errorf("edit: store commit: %w", err)
	}

	// Atomic compare-and-swap: only advance the branch if it still points
	// at baseCommitSHA. If another writer moved the ref in the meantime,
	// this fails with ErrReferenceHasChanged.
	oldRef := plumbing.NewHashReference(branchRef, baseHash)
	newRef := plumbing.NewHashReference(branchRef, newCommitHash)
	if err := st.CheckAndSetReference(newRef, oldRef); err != nil {
		if errors.Is(err, storage.ErrReferenceHasChanged) {
			return "", domain.ErrRefChanged
		}
		return "", fmt.Errorf("edit: set branch ref: %w", err)
	}

	return newCommitHash.String(), nil
}

// replaceTreeEntry returns a new tree hash where the entry at path segments
// (relative to tree) is replaced with newEntry. Intermediate directories are
// rebuilt; all other entries are preserved unchanged.
func replaceTreeEntry(repo *git.Repository, tree *object.Tree, segments []string, newEntry object.TreeEntry) (plumbing.Hash, error) {
	st := repo.Storer
	target := segments[0]

	if len(segments) == 1 {
		// Leaf: rebuild this tree with the one entry replaced.
		newEntries := make([]object.TreeEntry, 0, len(tree.Entries))
		found := false
		for _, e := range tree.Entries {
			if e.Name == target {
				newEntries = append(newEntries, newEntry)
				found = true
			} else {
				newEntries = append(newEntries, e)
			}
		}
		if !found {
			return plumbing.ZeroHash, domain.ErrPathNotFound
		}
		return encodeAndStoreTree(st, newEntries)
	}

	// Intermediate: find the sub-tree, recurse, then rebuild this tree
	// with the updated sub-tree entry.
	var subEntry *object.TreeEntry
	for i := range tree.Entries {
		if tree.Entries[i].Name == target {
			subEntry = &tree.Entries[i]
			break
		}
	}
	if subEntry == nil {
		return plumbing.ZeroHash, domain.ErrPathNotFound
	}
	if subEntry.Mode != filemode.Dir {
		return plumbing.ZeroHash, domain.ErrNotABlob
	}

	subTree, err := repo.TreeObject(subEntry.Hash)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("read subtree %q: %w", target, err)
	}

	newSubHash, err := replaceTreeEntry(repo, subTree, segments[1:], newEntry)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	newEntries := make([]object.TreeEntry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		if e.Name == target {
			newEntries = append(newEntries, object.TreeEntry{
				Name: target,
				Mode: filemode.Dir,
				Hash: newSubHash,
			})
		} else {
			newEntries = append(newEntries, e)
		}
	}
	return encodeAndStoreTree(st, newEntries)
}

// encodeAndStoreTree writes a tree object from the given entries and returns its hash.
func encodeAndStoreTree(storer storage.Storer, entries []object.TreeEntry) (plumbing.Hash, error) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	treeObj := storer.NewEncodedObject()
	t := &object.Tree{Entries: entries}
	if err := t.Encode(treeObj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tree: %w", err)
	}
	h, err := storer.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store tree: %w", err)
	}
	return h, nil
}
