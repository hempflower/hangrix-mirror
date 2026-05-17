package service

import (
	"context"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
)

// hostYAMLWithPromptFile uses prompt_file: instead of an inline prompt:.
// The spawner is supposed to load the referenced file from base-branch
// HEAD at spawn time so the snapshot is frozen.
const hostYAMLWithPromptFile = `version: 1
container:
  image: ghcr.io/acme/dev:1.2.3
llm:
  model: claude-sonnet-4-6
roles:
  backend:
    triggers: [issue.opened]
    can: [issue_read]
    prompt_file: .hangrix/prompts/backend.md
`

// TestSpawnerLoadsPromptFile confirms PromptFile resolution: the
// addendum on the persisted session must come from the file blob, not a
// hardcoded inline string. (If a future regression silently swallows
// the file content, this test fails.)
func TestSpawnerLoadsPromptFile(t *testing.T) {
	h := newTestSpawner(t, []byte(hostYAMLWithPromptFile), nil)
	h.blob.files["main:.hangrix/prompts/backend.md"] = []byte("You are the backend role. Push to issue/<n> only.")

	got, err := h.spawner.OnTrigger(context.Background(), domain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueOpened,
		CauseKind:   domain.CauseKindIssueOpened,
		RepoID:      1,
		IssueNumber: 1,
		ActorID:     1,
	})
	if err != nil {
		t.Fatalf("OnTrigger err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	addendum := h.runner.sessions[0].HostAddendum
	if !strings.HasPrefix(addendum, "You are the backend role.") {
		t.Fatalf("HostAddendum = %q, want it to start with the prompt-file body", addendum)
	}
}

// TestSpawnerRequiresLLMModel: a role + host both omit llm.model. The
// spawner refuses to write a row with an empty model column (the
// runner's env injection would emit `MODEL=` which the agent's LLM
// client would crash on).
func TestSpawnerRequiresLLMModel(t *testing.T) {
	yaml := `version: 1
container:
  image: ghcr.io/acme/dev:1.2.3
roles:
  backend:
    prompt: hi
    triggers: [issue.opened]
    can: [issue_read]
`
	h := newTestSpawner(t, []byte(yaml), nil)
	got, err := h.spawner.OnTrigger(context.Background(), domain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueOpened,
		CauseKind:   domain.CauseKindIssueOpened,
		RepoID:      1,
		IssueNumber: 1,
		ActorID:     1,
	})
	if err != nil {
		t.Fatalf("OnTrigger err: %v", err)
	}
	// Per-role failure is silent at the caller surface (no session
	// row) — that's the intentional best-effort stance.
	if len(got) != 0 {
		t.Fatalf("got %d sessions, want 0 when llm.model is unresolved", len(got))
	}
}

// TestSpawnerPerRoleLLMOverridesHost: role-level `llm.model` MUST win
// over the team-level default. The audit row stores the resolved model
// so changes here are observable downstream.
func TestSpawnerPerRoleLLMOverridesHost(t *testing.T) {
	yaml := `version: 1
container:
  image: ghcr.io/acme/dev:1.2.3
llm:
  model: claude-sonnet-4-6
roles:
  backend:
    prompt: hi
    triggers: [issue.opened]
    can: [issue_read]
    llm:
      model: claude-opus-4-7
`
	h := newTestSpawner(t, []byte(yaml), nil)
	_, err := h.spawner.OnTrigger(context.Background(), domain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueOpened,
		CauseKind:   domain.CauseKindIssueOpened,
		RepoID:      1,
		IssueNumber: 1,
		ActorID:     1,
	})
	if err != nil {
		t.Fatalf("OnTrigger err: %v", err)
	}
	if len(h.runner.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(h.runner.sessions))
	}
	if got := h.runner.sessions[0].Model; got != "claude-opus-4-7" {
		t.Fatalf("model = %q, want claude-opus-4-7 (per-role override)", got)
	}
}

// TestSpawnerHostYAMLInvalidReturnsError: a malformed host yaml is a
// whole-config error that propagates up so the issue handler can log.
// Other roles aren't tried — the file is bad as a whole.
func TestSpawnerHostYAMLInvalidReturnsError(t *testing.T) {
	h := newTestSpawner(t, []byte("not: valid: yaml:::"), nil)
	_, err := h.spawner.OnTrigger(context.Background(), domain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueOpened,
		CauseKind:   domain.CauseKindIssueOpened,
		RepoID:      1,
		IssueNumber: 1,
		ActorID:     1,
	})
	if err == nil {
		t.Fatalf("expected error on invalid host yaml")
	}
	if len(h.runner.sessions) != 0 {
		t.Fatalf("no sessions should be created on host yaml parse failure, got %d", len(h.runner.sessions))
	}
}

// TestSpawnerMultiRoleDeterministicOrder asserts spawn order is stable
// (lexicographic by role key). Audit consumers rely on this for
// readable timeline reproduction; the underlying map iteration is
// not stable, so the spawner sorts keys before iterating.
func TestSpawnerMultiRoleDeterministicOrder(t *testing.T) {
	yaml := `version: 1
container:
  image: ghcr.io/acme/dev:1.2.3
llm:
  model: claude-sonnet-4-6
roles:
  backend:
    prompt: hi
    triggers: [issue.opened]
    can: [issue_read]
  dispatcher:
    prompt: hi
    triggers: [issue.opened]
    can: [issue_read]
  reviewer:
    prompt: hi
    triggers: [issue.opened]
    can: [issue_read]
`
	h := newTestSpawner(t, []byte(yaml), nil)
	got, err := h.spawner.OnTrigger(context.Background(), domain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueOpened,
		CauseKind:   domain.CauseKindIssueOpened,
		RepoID:      1,
		IssueNumber: 1,
		ActorID:     1,
	})
	if err != nil {
		t.Fatalf("OnTrigger err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}
	want := []string{"backend", "dispatcher", "reviewer"}
	for i, w := range want {
		if got[i].RoleKey != w {
			t.Fatalf("position %d: role = %q, want %q", i, got[i].RoleKey, w)
		}
	}
}
