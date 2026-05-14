package infra

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	// Install the pre-receive hook so future pushes honor branch_protections.
	// The rules file is written lazily by SyncProtectionRules (called from
	// the receive-pack handler).
	if err := s.installPreReceiveHook(path); err != nil {
		return err
	}
	return nil
}

// preReceiveHookScript is the bash script we drop into hooks/pre-receive.
// It reads one rule per line from hooks/hangrix-protections (written by
// SyncProtectionRules just before each receive-pack run). Format per line:
//
//	<pattern> <forbid_force_push> <forbid_delete> <forbid_direct_push>
//
// where each flag is "1" or "0". The pattern is matched against the
// branch short-name via the shell's case glob (close enough to path.Match
// for the limited grammar we accept).
//
// Exits non-zero to reject the entire push if any ref violates a rule.
const preReceiveHookScript = `#!/bin/sh
# Hangrix pre-receive hook.
# Generated automatically. Do not edit — overwritten on every push.
rules_file="$(dirname "$0")/hangrix-protections"
zero="0000000000000000000000000000000000000000"
err=0

while read oldsha newsha refname; do
    case "$refname" in
        refs/heads/*) branch="${refname#refs/heads/}" ;;
        *) continue ;;
    esac

    if [ ! -f "$rules_file" ]; then
        continue
    fi

    while IFS=' ' read -r pat fp fd fdp; do
        [ -z "$pat" ] && continue
        case "$branch" in
            $pat)
                if [ "$newsha" = "$zero" ]; then
                    if [ "$fd" = "1" ]; then
                        echo "hangrix: branch '$branch' is protected against deletion (rule: $pat)" >&2
                        err=1
                    fi
                else
                    if [ "$oldsha" != "$zero" ] && [ "$fp" = "1" ]; then
                        if ! git merge-base --is-ancestor "$oldsha" "$newsha" 2>/dev/null; then
                            echo "hangrix: branch '$branch' rejects force-push (rule: $pat)" >&2
                            err=1
                        fi
                    fi
                fi
                ;;
        esac
    done < "$rules_file"
done

exit $err
`

// installPreReceiveHook writes the hook script into <repo>.git/hooks/
// idempotently. The script content is constant; we always rewrite it so
// upgrades pick up new versions automatically.
func (s *Storage) installPreReceiveHook(repoPath string) error {
	hooksDir := filepath.Join(repoPath, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("hooks: mkdir: %w", err)
	}
	hookPath := filepath.Join(hooksDir, "pre-receive")
	if err := os.WriteFile(hookPath, []byte(preReceiveHookScript), 0o755); err != nil {
		return fmt.Errorf("hooks: write pre-receive: %w", err)
	}
	return nil
}

// SyncProtectionRules writes the current protection ruleset into
// hooks/hangrix-protections so the pre-receive hook can read it. Call this
// right before invoking git-receive-pack. Also re-installs the hook script
// so a repo created before this feature shipped picks it up on first push.
func (s *Storage) SyncProtectionRules(repoPath string, rules []*domain.BranchProtection) error {
	if err := s.installPreReceiveHook(repoPath); err != nil {
		return err
	}
	hooksDir := filepath.Join(repoPath, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("hooks: mkdir: %w", err)
	}
	rulesPath := filepath.Join(hooksDir, "hangrix-protections")
	var b strings.Builder
	for _, r := range rules {
		// Pattern is already validated upstream (no whitespace allowed),
		// so a naive space-separated encoding round-trips safely.
		fmt.Fprintf(&b, "%s %s %s %s\n",
			r.Pattern,
			boolFlag(r.ForbidForcePush),
			boolFlag(r.ForbidDelete),
			boolFlag(r.ForbidDirectPush),
		)
	}
	if err := os.WriteFile(rulesPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("hooks: write rules: %w", err)
	}
	return nil
}

func boolFlag(v bool) string {
	if v {
		return "1"
	}
	return "0"
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
