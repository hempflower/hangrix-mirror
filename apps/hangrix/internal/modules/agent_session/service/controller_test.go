package service

import (
	"context"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// newTestController wires a Controller with a stub runner repo + real
// cryptobox (using the testEncryptionKey from spawner_test.go).
func newTestController(t *testing.T) (*Controller, *stubRunnerRepo) {
	t.Helper()
	cfg := &config.Config{
		LLM: config.LLMConfig{EncryptionKey: testEncryptionKey},
	}
	runner := newStubRunnerRepo()
	ctrl := NewController(&ControllerDeps{Runner: runner, Config: cfg})
	return ctrl, runner
}

// seedSession is a helper that inserts one session row into the stub repo
// with the given status and sealed value.
func seedSession(r *stubRunnerRepo, status runnerdomain.SessionStatus, sealed string) *runnerdomain.AgentSession {
	sess := &runnerdomain.AgentSession{
		ID:                 r.nextID,
		RunnerID:           nil,
		RepoID:             intPtr(1),
		IssueNumber:        int32Ptr(42),
		Status:             status,
		Role:               "server",
		Model:              "claude-sonnet-4-6",
		SessionTokenPrefix: "hgxs_aaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SessionTokenHash:   "$2a$10$...existing...",
		SessionTokenSealed: sealed,
	}
	r.nextID++
	r.sessions = append(r.sessions, sess)
	return sess
}

func intPtr(v int64) *int64  { return &v }
func int32Ptr(v int32) *int32 { return &v }

// ---- tests ----

// TestControllerResumeReusesExistingToken asserts that when a session has
// a non-empty session_token_sealed, Resume() reuses the existing prefix /
// hash / sealed instead of minting a new token.
func TestControllerResumeReusesExistingToken(t *testing.T) {
	ctrl, runner := newTestController(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusFailed, "enc:old-sealed-plaintext")

	wantPrefix := sess.SessionTokenPrefix
	wantHash := sess.SessionTokenHash
	wantSealed := sess.SessionTokenSealed

	if err := ctrl.Resume(ctx, sess.ID); err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}

	got := runner.sessions[0]
	if got.Status != runnerdomain.SessionStatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
	if got.SessionTokenPrefix != wantPrefix {
		t.Errorf("prefix changed: got %q, want %q", got.SessionTokenPrefix, wantPrefix)
	}
	if got.SessionTokenHash != wantHash {
		t.Errorf("hash changed: got %q, want %q", got.SessionTokenHash, wantHash)
	}
	if got.SessionTokenSealed != wantSealed {
		t.Errorf("sealed changed: got %q, want %q", got.SessionTokenSealed, wantSealed)
	}
	if got.ErrorMessage != "" {
		t.Errorf("error_message = %q, want empty", got.ErrorMessage)
	}
	if got.RunnerID != nil {
		t.Errorf("runner_id = %v, want nil", got.RunnerID)
	}
	if got.ClaimedAt != nil || got.StartedAt != nil || got.EndedAt != nil {
		t.Error("claimed_at / started_at / ended_at should be nil after resume")
	}
}

// TestControllerResumeMintsNewTokenWhenSealedEmpty asserts the fallback
// path: when a session's sealed is empty (legacy row or pre-sealed-
// preservation data), Resume() mints a fresh token.
func TestControllerResumeMintsNewTokenWhenSealedEmpty(t *testing.T) {
	ctrl, runner := newTestController(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusFailed, "")

	oldPrefix := sess.SessionTokenPrefix
	oldHash := sess.SessionTokenHash

	if err := ctrl.Resume(ctx, sess.ID); err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}

	got := runner.sessions[0]
	if got.Status != runnerdomain.SessionStatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
	if got.SessionTokenPrefix == oldPrefix {
		t.Errorf("prefix should have changed (was %q, still %q) — sealed was empty", oldPrefix, got.SessionTokenPrefix)
	}
	if got.SessionTokenHash == oldHash {
		t.Errorf("hash should have changed (was %q, still %q) — sealed was empty", oldHash, got.SessionTokenHash)
	}
	if got.SessionTokenSealed == "" {
		t.Errorf("sealed should be non-empty after fresh mint")
	}
	// The freshly minted prefix is 8 alphanum chars (MintSessionToken
	// returns the bare prefix; the hgxs_ wrapper is on the wire plaintext).
	if len(got.SessionTokenPrefix) != 8 {
		t.Errorf("fresh prefix length = %d, want 8: %q", len(got.SessionTokenPrefix), got.SessionTokenPrefix)
	}
}

// TestControllerResumeOnArchivedReturnsError asserts archived sessions
// cannot be resumed.
func TestControllerResumeOnArchivedReturnsError(t *testing.T) {
	ctrl, runner := newTestController(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusArchived, "enc:sealed")

	err := ctrl.Resume(ctx, sess.ID)
	if err != domain.ErrNotResumable {
		t.Fatalf("expected ErrNotResumable, got %v", err)
	}
}

// TestControllerResumeOnLiveReturnsError asserts live (pending/claimed/
// running) sessions cannot be resumed.
func TestControllerResumeOnLiveReturnsError(t *testing.T) {
	ctx := context.Background()

	for _, status := range []runnerdomain.SessionStatus{
		runnerdomain.SessionStatusPending,
		runnerdomain.SessionStatusClaimed,
		runnerdomain.SessionStatusRunning,
	} {
		// fresh repo for each to avoid duplicate id panics
		ctrl2, runner2 := newTestController(t)
		sess := seedSession(runner2, status, "enc:sealed")
		err := ctrl2.Resume(ctx, sess.ID)
		if err != domain.ErrNotResumable {
			t.Errorf("status %q: expected ErrNotResumable, got %v", status, err)
		}
	}
}

// TestControllerRecoverReusesExistingToken asserts Recover() also
// preserves token identity when sealed is available.
func TestControllerRecoverReusesExistingToken(t *testing.T) {
	ctrl, runner := newTestController(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusFailed, "enc:sealed-recover")

	wantPrefix := sess.SessionTokenPrefix
	wantHash := sess.SessionTokenHash
	wantSealed := sess.SessionTokenSealed

	if err := ctrl.Recover(ctx, sess.ID, "server"); err != nil {
		t.Fatalf("Recover returned error: %v", err)
	}

	got := runner.sessions[0]
	if got.Status != runnerdomain.SessionStatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
	if got.SessionTokenPrefix != wantPrefix {
		t.Errorf("prefix changed: got %q, want %q", got.SessionTokenPrefix, wantPrefix)
	}
	if got.SessionTokenHash != wantHash {
		t.Errorf("hash changed: got %q, want %q", got.SessionTokenHash, wantHash)
	}
	if got.SessionTokenSealed != wantSealed {
		t.Errorf("sealed changed: got %q, want %q", got.SessionTokenSealed, wantSealed)
	}

	// Verify the recover event was enqueued.
	found := false
	for _, in := range runner.inputs {
		if string(in.Payload) == `{"event":"manual.recover","kind":"event","payload":{"reason":"agent-initiated recovery","recovered_by":"server"}}` {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("recover event not found in inputs queue; got %d inputs", len(runner.inputs))
	}
}

// TestControllerResumeEnqueuesEvent verifies the manual.resume event
// frame is enqueued on the inputs queue.
func TestControllerResumeEnqueuesEvent(t *testing.T) {
	ctrl, runner := newTestController(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusFailed, "enc:sealed")

	if err := ctrl.Resume(ctx, sess.ID); err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}

	if len(runner.inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(runner.inputs))
	}
	payload := string(runner.inputs[0].Payload)
	if payload != `{"event":"manual.resume","kind":"event","payload":{"reason":"user resume"}}` {
		t.Errorf("unexpected event frame: %s", payload)
	}
}
