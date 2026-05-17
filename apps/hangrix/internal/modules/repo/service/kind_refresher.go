// Package service hosts the repo module's stateless helpers that other
// modules consume via narrow domain interfaces. KindRefresher is the
// first such helper: it inspects the default-branch tip for a root
// agent.yml and reclassifies the repo accordingly.
//
// Used by:
//   - repo/handler git smart-http post-receive (recomputes kind after every push)
//   - issue/handler merge endpoint (the merge path doesn't go through
//     receive-pack, so it has to nudge the refresh explicitly)
//
// Pulled out of repo/handler/git_http.go in M7a Phase 2 because the
// issue module can't import repo/handler (circular: repo/handler depends
// on the issue module's PushObserver). Living here lets both call sites
// share the same code.
package service

import (
	"context"
	"io"
	"os/exec"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// KindRefresher recomputes domain.Kind for a repo based on its current
// default-branch tip. All errors are swallowed: a failed parse / missing
// blob / DB hiccup just leaves the cached kind one push stale.
type KindRefresher struct {
	store domain.Store
}

type KindRefresherDeps struct {
	Store domain.Store
}

func NewKindRefresher(deps *KindRefresherDeps) *KindRefresher {
	return &KindRefresher{store: deps.Store}
}

// Refresh inspects <default_branch>:agent.yml at fsPath and updates
// repos.kind accordingly. KindAgent iff the blob exists AND parses
// against the strict agents_config schema. Any failure path → KindStandard
// (an owner can still push a fix because the receive-pack guard never
// checks the kind column).
func (r *KindRefresher) Refresh(ctx context.Context, repo *domain.Repo, fsPath string) {
	branch := repo.DefaultBranch
	if branch == "" {
		return
	}
	kind := domain.KindStandard
	if body, ok := readBlobAtRef(ctx, fsPath, branch, "agent.yml"); ok {
		if _, err := agentsconfig.ParseAgentManifest(body); err == nil {
			kind = domain.KindAgent
		}
	}
	_ = r.store.UpdateKind(ctx, repo.ID, kind)
}

// readBlobAtRef returns the bytes of <ref>:<path> via `git cat-file -p`.
// Mirrors the helper that used to live in repo/handler/git_http.go —
// kept tiny here so this package doesn't have to expose it.
func readBlobAtRef(ctx context.Context, fsPath, ref, path string) ([]byte, bool) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+fsPath,
		"cat-file",
		"-p",
		ref+":"+path,
	)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}
