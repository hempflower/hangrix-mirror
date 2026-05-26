package handler

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"strings"

	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
)

// emptyTreeSHA is the git hash of the empty tree — a well-known constant.
// When diffing against it, git produces the full set of files that exist at
// the other tree-ish, which is exactly what we need for a new-branch push
// where the old SHA is the all-zero hash.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// PushObserver implements repodomain.PushObserver. In PostReceive it scans
// pushed tag refs and triggers repo.push_tag workflow runs via the service.
//
// We expose the observer as a thin wrapper rather than directly satisfying
// the interface from *Handler so the ioc binding stays explicit — the
// container resolves []PushObserver, and only this wrapper appears in that
// slice.
type PushObserver struct {
	svc *service.Service
}

// PushObserverDeps wires the observer's single dependency through ioc.
type PushObserverDeps struct {
	Service *service.Service
}

// NewPushObserver creates a workflow PushObserver.
func NewPushObserver(deps *PushObserverDeps) *PushObserver {
	return &PushObserver{svc: deps.Service}
}

// PreReceive is a no-op for the workflow observer.
func (o *PushObserver) PreReceive(ctx context.Context, repo *repodomain.Repo, fsPath string, refUpdates []repodomain.PushRefUpdate) error {
	return nil
}

// PostReceive scans pushed refs and triggers matching workflow runs.
// Tag refs → repo.push_tag; branch refs → repo.push (via DispatchRepoPush).
// Deletions (zero NewSHA) are skipped. Failures are logged but do not
// affect the push outcome — PostReceive runs after the receive-pack
// subprocess has already returned success to the client.
func (o *PushObserver) PostReceive(ctx context.Context, repo *repodomain.Repo, fsPath string, pusher repodomain.Pusher, refUpdates []repodomain.PushRefUpdate) ([]repodomain.PostReceiveContrib, error) {
	for _, u := range refUpdates {
		refName := u.RefName
		if isZeroSHA(u.NewSHA) {
			continue // skip deletions
		}

		switch {
		case strings.HasPrefix(refName, "refs/tags/"):
			tagName := strings.TrimPrefix(refName, "refs/tags/")
			if tagName == "" {
				continue
			}
			log.Printf("workflow: PostReceive tag=%s sha=%s repo=%d", tagName, u.NewSHA, repo.ID)
			if err := o.svc.TriggerTagEvent(ctx, repo.ID, repo.OwnerName, repo.Name, repo.DefaultBranch, tagName, u.NewSHA); err != nil {
				log.Printf("workflow: PostReceive tag event trigger %s: %v", tagName, err)
			}

		case strings.HasPrefix(refName, "refs/heads/"):
			branch := strings.TrimPrefix(refName, "refs/heads/")
			if branch == "" {
				continue
			}
			log.Printf("workflow: PostReceive branch=%s old=%s new=%s repo=%d", branch, u.OldSHA, u.NewSHA, repo.ID)

			changedPaths := branchChangedPaths(ctx, fsPath, u.OldSHA, u.NewSHA)
			triggerPayload, _ := json.Marshal(map[string]any{
				"event":         "repo.push",
				"branch":        branch,
				"commit_sha":    u.NewSHA,
				"changed_paths": changedPaths,
				"pusher_user_id": pusher.UserID,
				"pusher_agent_role": pusher.AgentRole,
			})

			o.svc.DispatchRepoPush(ctx,
				service.Ref{
					ID:            repo.ID,
					Name:          repo.Name,
					DefaultBranch: repo.DefaultBranch,
					OwnerName:     repo.OwnerName,
				},
				branch,
				changedPaths,
				triggerPayload,
				u.NewSHA,
			)
		}
	}
	return nil, nil
}

// branchChangedPaths returns the list of file paths changed between oldSHA
// and newSHA. When oldSHA is the git zero-hash (new branch creation) we diff
// against the well-known empty-tree SHA so that ALL files in the branch
// tip are included, covering multi-commit branch pushes. Otherwise git diff
// --name-only gives the symmetric difference. Returns nil on any error —
// changed paths are a best-effort signal for workflow path filters.
func branchChangedPaths(ctx context.Context, fsPath, oldSHA, newSHA string) []string {
	oldRef := oldSHA
	if isZeroSHA(oldSHA) {
		// New branch: diff empty tree → newSHA to capture every file
		// in the branch tip, not just changes in the tip commit.
		oldRef = emptyTreeSHA
	}
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+fsPath,
		"diff",
		"--name-only",
		oldRef,
		newSHA,
	)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("workflow: PostReceive branch changed paths %s..%s: %v", oldSHA, newSHA, err)
		return nil
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// isZeroSHA reports whether sha is the git zero-hash (40 '0' chars),
// which signals a ref deletion in the receive-pack protocol.
func isZeroSHA(sha string) bool {
	return sha == "0000000000000000000000000000000000000000"
}
