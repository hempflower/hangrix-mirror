package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/service"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// Controller is the user-facing session-lifecycle service: Stop,
// Resume, Delete. Composed onto runner.Repo + cryptobox so it can mint
// a fresh sealed session token on Resume the same way the spawner
// does. Separate from Spawner because the call sites are different —
// Spawner reacts to upstream events; Controller reacts to UI buttons.
type Controller struct {
	runner runnerdomain.Repo
	box    *cryptobox.Box
}

type ControllerDeps struct {
	Runner runnerdomain.Repo
	Config *config.Config
}

func NewController(deps *ControllerDeps) *Controller {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(fmt.Errorf("agent_session controller: %w", err))
	}
	return &Controller{runner: deps.Runner, box: box}
}

// Stop satisfies domain.Controller.
//
// Flow:
//  1. Look up the session — 404 if missing.
//  2. If already terminal/archived, return nil (idempotent: UI may
//     click stop on a session that just exited on its own).
//  3. Enqueue a control:shutdown frame so a running container exits
//     cleanly when it next polls /inputs. Failure is logged on the
//     enqueue path but doesn't block the mark — worst case the
//     container keeps running until it hits an idle gap.
//  4. Mark the session 'failed' with an explanation message so the
//     audit row records who asked for the stop.
func (c *Controller) Stop(ctx context.Context, sessionID int64, reason string) error {
	sess, err := c.runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.Status.Terminal() || sess.Status == runnerdomain.SessionStatusArchived {
		return nil
	}
	frame, _ := json.Marshal(map[string]any{
		"kind": "control",
		"op":   "shutdown",
	})
	if _, err := c.runner.EnqueueInput(ctx, sessionID, frame); err != nil {
		// Non-fatal: the container will eventually be killed when the
		// orchestrator notices the session is failed (a later
		// milestone). For now we keep going so the session shows up
		// as failed in the UI even if enqueue raced.
	}
	msg := reason
	if msg == "" {
		msg = "stopped by user"
	}
	if err := c.runner.MarkSessionTerminal(ctx, sessionID, runnerdomain.SessionStatusFailed, nil, msg); err != nil {
		if errors.Is(err, runnerdomain.ErrSessionStateInvalid) {
			// Race with the runner's own terminate: session went
			// terminal between the GetSessionByID check and our
			// mark. Treat as success — caller's intent is met.
			return nil
		}
		return err
	}
	// Record the stop event on the message log so the audit timeline
	// reflects the manual cancellation.
	_, _ = c.runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: sessionID,
		Kind:      runnerdomain.MessageKindSystem,
		Content:   msg,
	})
	return nil
}

// Resume satisfies domain.Controller. Mints a fresh sealed session
// token and flips an idle / failed / succeeded row back to 'pending'.
func (c *Controller) Resume(ctx context.Context, sessionID int64) error {
	sess, err := c.runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		return err
	}
	switch sess.Status {
	case runnerdomain.SessionStatusArchived:
		return domain.ErrNotResumable
	case runnerdomain.SessionStatusPending,
		runnerdomain.SessionStatusClaimed,
		runnerdomain.SessionStatusRunning:
		return domain.ErrNotResumable
	}
	plaintext, prefix, hashed, err := service.MintSessionToken()
	if err != nil {
		return fmt.Errorf("mint session token: %w", err)
	}
	sealed, err := c.box.Encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("seal session token: %w", err)
	}
	if err := c.runner.ResumeSession(ctx, sessionID, runnerdomain.NewSessionToken{
		Prefix: prefix,
		Hash:   string(hashed),
		Sealed: sealed,
	}); err != nil {
		return err
	}
	// Seed a fresh history frame so the agent loop's first-frame
	// invariant holds. We deliberately don't replay the prior message
	// log — that's an M9 follow-up; the agent will see the most
	// recent comment context via the issue's comment thread when it
	// uses platform tools.
	history := []byte(`{"kind":"history","messages":[]}`)
	if _, err := c.runner.EnqueueInput(ctx, sessionID, history); err != nil {
		return fmt.Errorf("enqueue history on resume: %w", err)
	}
	// Synthetic manual cause so the agent has an event to react to.
	frame, _ := json.Marshal(map[string]any{
		"kind":  "event",
		"event": "manual.resume",
		"payload": map[string]any{
			"reason": "user resume",
		},
	})
	if _, err := c.runner.EnqueueInput(ctx, sessionID, frame); err != nil {
		return fmt.Errorf("enqueue cause on resume: %w", err)
	}
	_, _ = c.runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: sessionID,
		Kind:      runnerdomain.MessageKindSystem,
		Content:   "resumed by user",
	})
	return nil
}

// Delete satisfies domain.Controller. Refuses live sessions to keep
// runner-side state coherent: a runner that just claimed the row would
// 500 on its next AppendMessage if we deleted from under it.
//
// Container-aware: when the session owns a long-lived container (see
// migration 00004), hard-DELETE would strand the container — the
// runner's cleanup poll keys off agent_sessions.runner_id, and a deleted
// row has nothing to match. We instead archive the row (so the user
// sees it leave their active list) and flag the container for cleanup;
// the runner's sweeper picks it up on its next poll and `docker rm`s.
// A future commit can add a "purge archived" sweeper to hard-DELETE
// these rows once the container is gone — for now they stay archived,
// which is cheap (zero non-row state) and audit-friendly.
func (c *Controller) Delete(ctx context.Context, sessionID int64) error {
	sess, err := c.runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		return err
	}
	switch sess.Status {
	case runnerdomain.SessionStatusPending,
		runnerdomain.SessionStatusClaimed,
		runnerdomain.SessionStatusRunning:
		return domain.ErrSessionLive
	}
	if sess.ContainerID != "" {
		return c.runner.ArchiveSessionByID(ctx, sessionID)
	}
	return c.runner.DeleteSession(ctx, sessionID)
}

var _ domain.Controller = (*Controller)(nil)
