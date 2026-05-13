package infra

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"

	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
)

// ErrUnsafePath is returned by Storage when a supplied owner username or repo
// name fails filesystem-safety validation. Callers MUST surface this rather
// than continuing — never join unvalidated user input into a filesystem path.
var ErrUnsafePath = errors.New("unsafe path component")

// fsSafe matches the same character class accepted for repo names and
// usernames at the handler layer. Anchored to the full string; explicitly
// rejects "..".
var fsSafe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Storage is a thin wrapper around the bare-repo filesystem layout. It does
// not touch the metadata DB. The handler composes Storage with domain.Store.
type Storage struct {
	reposPath string
	git       gitdomain.Git
}

type StorageDeps struct {
	Config *config.Config
	Git    gitdomain.Git
}

func NewStorage(deps *StorageDeps) *Storage {
	return &Storage{
		reposPath: deps.Config.Storage.ReposPath,
		git:       deps.Git,
	}
}

// ResolvePath returns the absolute on-disk location for a bare repo. Both
// path components are validated; an unsafe component returns ErrUnsafePath.
// The result is cleaned to collapse any redundant separators.
func (s *Storage) ResolvePath(ownerUsername, repoName string) (string, error) {
	if !safeComponent(ownerUsername) || !safeComponent(repoName) {
		return "", ErrUnsafePath
	}
	return filepath.Clean(filepath.Join(s.reposPath, ownerUsername, repoName+".git")), nil
}

// InitOnDisk creates the bare repository for repo and, if seedReadme is true,
// adds an initial commit so the repo can be cloned immediately. Author
// identity is recorded on the seed commit only.
func (s *Storage) InitOnDisk(repo *domain.Repo, ownerUsername string, seedReadme bool, authorName, authorEmail string) error {
	path, err := s.ResolvePath(ownerUsername, repo.Name)
	if err != nil {
		return err
	}
	if err := s.git.Init(path, repo.DefaultBranch); err != nil {
		return err
	}
	if seedReadme {
		if err := s.git.SeedReadme(path, repo.DefaultBranch, repo.Name, repo.Description, authorName, authorEmail); err != nil {
			return err
		}
	}
	return nil
}

// DeleteOnDisk removes the bare repository directory. Returns nil if the
// directory does not exist; that mirrors os.RemoveAll's behavior and lets
// the DELETE handler stay idempotent against a missing on-disk artifact.
func (s *Storage) DeleteOnDisk(ownerUsername, repoName string) error {
	path, err := s.ResolvePath(ownerUsername, repoName)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

// Git exposes the underlying git interface so the handler can issue read
// operations against a resolved path without re-wiring its own dependency.
func (s *Storage) Git() gitdomain.Git { return s.git }

func safeComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	return fsSafe.MatchString(s)
}
