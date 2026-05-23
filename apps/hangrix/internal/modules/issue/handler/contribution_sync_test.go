package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	gitinfra "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// fakeIssueStore embeds domain.Store so it satisfies the interface; only the
// three methods SyncContribution touches are implemented (the rest panic if
// ever called, which would surface an unexpected dependency).
type fakeIssueStore struct {
	domain.Store
	iss *domain.Issue
}

func (f *fakeIssueStore) GetByNumber(_ context.Context, _, _ int64) (*domain.Issue, error) {
	return f.iss, nil
}
func (f *fakeIssueStore) ListEvents(_ context.Context, _ int64) ([]*domain.Event, error) {
	return nil, nil
}
func (f *fakeIssueStore) CreateAgentEvent(_ context.Context, _ int64, _ domain.EventKind, _ []byte, _ string) (*domain.Event, error) {
	return &domain.Event{}, nil
}

type fakeContribStore struct {
	domain.ContributionStore
	upserted *domain.ContributionUpsertParams
	row      *domain.Contribution
}

func (f *fakeContribStore) UpsertContributionOnPush(_ context.Context, p domain.ContributionUpsertParams) (*domain.Contribution, error) {
	f.upserted = &p
	f.row = &domain.Contribution{ID: 1, IssueID: p.IssueID, RefName: p.RefName, AgentRole: p.AgentRole, ChangedPaths: p.ChangedPaths, Status: domain.ContribStatusPending}
	return f.row, nil
}
func (f *fakeContribStore) SetContributionMergeable(_ context.Context, _ int64, _ bool, _ string) error {
	return nil
}
func (f *fakeContribStore) SetContributionStatus(_ context.Context, _ int64, _ domain.ContributionStatus) (*domain.Contribution, error) {
	return f.row, nil
}
func (f *fakeContribStore) GetContributionByRef(_ context.Context, _ int64, _ string) (*domain.Contribution, error) {
	return nil, domain.ErrContributionNotFound
}

// TestSyncContributionRecognizesPush exercises the recognition logic against a
// real BARE repo (what the server actually serves), populated by a real
// `git push` of an issue/42 branch and a contribution branch
// issue-42/server/fix — i.e. the freshly-pushed nested ref the server diffs.
// SyncContribution must upsert a contribution row with the right role + a
// non-empty real diff.
func TestSyncContributionRecognizesPush(t *testing.T) {
	barePath, fixSHA := setupBareWithContribution(t)

	cstore := &fakeContribStore{}
	h := &Handler{
		issues:        &fakeIssueStore{iss: &domain.Issue{ID: 7, Number: 42, BranchName: "issue/42", State: domain.StateOpen}},
		contributions: cstore,
		git:           gitinfra.NewGoGit(nil),
	}

	h.SyncContribution(context.Background(), &repodomain.Repo{ID: 1}, barePath, repodomain.PushRefUpdate{
		RefName: "refs/heads/issue-42/server/fix",
		OldSHA:  strings.Repeat("0", 40),
		NewSHA:  fixSHA,
	})

	if cstore.upserted == nil {
		t.Fatal("contribution NOT recognized: UpsertContributionOnPush was never called for a valid issue-42/server/fix push")
	}
	if cstore.upserted.AgentRole != "server" {
		t.Errorf("AgentRole = %q, want %q", cstore.upserted.AgentRole, "server")
	}
	if cstore.upserted.RefName != "refs/heads/issue-42/server/fix" {
		t.Errorf("RefName = %q", cstore.upserted.RefName)
	}
	if len(cstore.upserted.ChangedPaths) == 0 {
		t.Errorf("ChangedPaths empty — real diff not computed against the bare repo: %+v", cstore.upserted)
	}
}

// TestContributionDiffStatsNeverNil guards the regression: a nil ChangedPaths
// is encoded by pgx as SQL NULL, which fails the changed_paths TEXT[] NOT NULL
// constraint and silently aborts the upsert. The stats helper must return a
// non-nil slice even for an empty/nil diff.
func TestContributionDiffStatsNeverNil(t *testing.T) {
	if cp, _, _, _ := contributionDiffStats(nil); cp == nil {
		t.Error("contributionDiffStats(nil) returned a nil ChangedPaths slice")
	}
	if cp, _, _, _ := contributionDiffStats([]*gitdomain.FileDiff{}); cp == nil {
		t.Error("contributionDiffStats(empty) returned a nil ChangedPaths slice")
	}
}

// TestSyncContributionRecognizesEvenWithUncomputableDiff covers the failure
// path that produced the bug: when DiffMergeBase can't be computed (here the
// issue branch the contribution is diffed against doesn't exist on the server)
// the diff is empty — but the contribution must STILL be recognised, with a
// non-nil ChangedPaths so the NOT NULL upsert succeeds.
func TestSyncContributionRecognizesEvenWithUncomputableDiff(t *testing.T) {
	barePath, fixSHA := setupBareWithContribution(t)

	cstore := &fakeContribStore{}
	h := &Handler{
		// BranchName points at a branch that doesn't exist on the bare repo,
		// so DiffMergeBase errors and the diff comes back empty.
		issues:        &fakeIssueStore{iss: &domain.Issue{ID: 7, Number: 42, BranchName: "issue/does-not-exist", State: domain.StateOpen}},
		contributions: cstore,
		git:           gitinfra.NewGoGit(nil),
	}

	h.SyncContribution(context.Background(), &repodomain.Repo{ID: 1}, barePath, repodomain.PushRefUpdate{
		RefName: "refs/heads/issue-42/server/fix",
		OldSHA:  strings.Repeat("0", 40),
		NewSHA:  fixSHA,
	})

	if cstore.upserted == nil {
		t.Fatal("contribution NOT recognized when the diff was uncomputable")
	}
	if cstore.upserted.ChangedPaths == nil {
		t.Error("ChangedPaths is nil — pgx would encode this as NULL and fail the NOT NULL upsert")
	}
}

// setupBareWithContribution builds a bare repo (the server's view) and pushes
// into it, via real git, a `main`, an `issue/42` branch, and a contribution
// branch `issue-42/server/fix` that modifies a file. Returns the bare repo
// path and the contribution head SHA.
func setupBareWithContribution(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "srv.git")
	work := filepath.Join(root, "work")

	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_TERMINAL_PROMPT=0",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	git("", "init", "-q", "--bare", "-b", "main", bare)
	git("", "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "a.txt")
	git(work, "commit", "-q", "-m", "base")
	// main + issue/42 land on the server, exactly like issue-open's CreateBranch.
	git(work, "push", "-q", bare, "main:refs/heads/main", "main:refs/heads/issue/42")
	// Contribution branch off the issue branch, with a real change, pushed to
	// its namespace ref — the freshly-created nested ref on the bare repo.
	git(work, "checkout", "-q", "-b", "issue-42/server/fix")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("base\nchange\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "commit", "-q", "-am", "change")
	git(work, "push", "-q", bare, "HEAD:refs/heads/issue-42/server/fix")
	return bare, git(work, "rev-parse", "HEAD")
}
