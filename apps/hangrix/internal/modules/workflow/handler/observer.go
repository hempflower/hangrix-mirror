package handler

import (
	"context"
	"log"
	"strings"

	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
)

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

// PostReceive scans pushed tag refs and triggers matching repo.push_tag
// workflow runs. Non-tag refs and tag deletions (zero NewSHA) are ignored.
// Failures are logged but do not affect the push outcome — PostReceive runs
// after the receive-pack subprocess has already returned success to the client.
func (o *PushObserver) PostReceive(ctx context.Context, repo *repodomain.Repo, fsPath string, pusher repodomain.Pusher, refUpdates []repodomain.PushRefUpdate) ([]repodomain.PostReceiveContrib, error) {
	for _, u := range refUpdates {
		refName := u.RefName
		if !strings.HasPrefix(refName, "refs/tags/") {
			continue
		}
		tagName := strings.TrimPrefix(refName, "refs/tags/")
		if tagName == "" || isZeroSHA(u.NewSHA) {
			continue // skip deletions
		}

		log.Printf("workflow: PostReceive tag=%s sha=%s repo=%d", tagName, u.NewSHA, repo.ID)
		if err := o.svc.TriggerTagEvent(ctx, repo.ID, repo.OwnerName, repo.Name, repo.DefaultBranch, tagName, u.NewSHA); err != nil {
			log.Printf("workflow: PostReceive tag event trigger %s: %v", tagName, err)
		}
	}
	return nil, nil
}

// isZeroSHA reports whether sha is the git zero-hash (40 '0' chars),
// which signals a ref deletion in the receive-pack protocol.
func isZeroSHA(sha string) bool {
	return sha == "0000000000000000000000000000000000000000"
}
