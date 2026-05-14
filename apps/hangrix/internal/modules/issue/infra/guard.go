package infra

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// IssueGuard implements repodomain.BranchWriteGuard with the M4 rules:
//
//   - Pushes to a repo's base branch are rejected unless flagged as
//     IsInternal (the merge endpoint sets the flag when fast-forwarding
//     into base).
//   - Pushes to branches that don't match the `issue/<n>` pattern are
//     rejected outright.
//   - For `issue/<n>` branches the captured n must resolve to an open
//     issue inside the same repo.
//
// The guard mirrors what the pre-receive shell hook enforces — both ends
// have to agree so the web "create branch" button can be rejected with a
// meaningful error and git push attempts hit the same wall before changes
// touch the repo.
type IssueGuard struct {
	issues domain.Store
	repos  repodomain.Store
}

type IssueGuardDeps struct {
	Issues domain.Store
	Repos  repodomain.Store
}

func NewIssueGuard(deps *IssueGuardDeps) *IssueGuard {
	return &IssueGuard{issues: deps.Issues, repos: deps.Repos}
}

func (g *IssueGuard) CheckBranchWrite(ctx context.Context, op repodomain.BranchWriteOp) error {
	if op.IsInternal {
		return nil
	}

	repo, err := g.repos.GetByID(ctx, op.RepoID)
	if err != nil {
		return fmt.Errorf("issue guard: lookup repo: %w", err)
	}

	// Base branch is merge-only.
	if op.Branch == repo.DefaultBranch {
		return fmt.Errorf("%w: %q is the protected base branch; merge through an issue instead", repodomain.ErrBranchWriteDenied, op.Branch)
	}

	// Only `issue/<n>` patterns are allowed.
	const prefix = "issue/"
	if !strings.HasPrefix(op.Branch, prefix) {
		return fmt.Errorf("%w: branch %q is not bound to an issue; open one and push to issue/<n>", repodomain.ErrBranchWriteDenied, op.Branch)
	}
	rest := strings.TrimPrefix(op.Branch, prefix)
	if rest == "" || strings.ContainsRune(rest, '/') {
		return fmt.Errorf("%w: branch %q does not match issue/<n>", repodomain.ErrBranchWriteDenied, op.Branch)
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("%w: branch %q does not match issue/<n>", repodomain.ErrBranchWriteDenied, op.Branch)
	}

	issue, err := g.issues.GetByNumber(ctx, op.RepoID, n)
	if err != nil {
		return fmt.Errorf("%w: no open issue #%d", repodomain.ErrBranchWriteDenied, n)
	}
	if issue.State != domain.StateOpen {
		return fmt.Errorf("%w: issue #%d is %s; reopen or open a new issue to keep pushing", repodomain.ErrBranchWriteDenied, n, issue.State)
	}
	return nil
}
