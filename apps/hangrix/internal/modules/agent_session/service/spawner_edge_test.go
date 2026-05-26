package service

import (
	"context"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
)

// TestSpawnerLoadsRolePrompt confirms prompt resolution under the new
// contract: a role's prompt is the Markdown body of its
// `.hangrix/agents/<role>.md` file, loaded and frozen into role.Prompt by
// LoadHostConfig and copied verbatim onto the persisted session's
// HostAddendum. (If a future regression silently swallows the body, this
// test fails.)
func TestSpawnerLoadsRolePrompt(t *testing.T) {
	fixture := &hostFixture{
		yaml: teamYAML,
		roles: map[string]string{
			"backend": agentMD(
				"triggers:\n  issue.opened: {}\npermission: read\ntools: [all]",
				"You are the backend role. Push to issue/<n> only."),
		},
	}
	h := newTestSpawner(t, fixture, nil)

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
		t.Fatalf("HostAddendum = %q, want it to start with the role prompt body", addendum)
	}
}

// TestSpawnerRequiresLLMModel: a role + host both omit llm.model. The
// spawner refuses to write a row with an empty model column (the
// runner's env injection would emit `MODEL=` which the agent's LLM
// client would crash on).
func TestSpawnerRequiresLLMModel(t *testing.T) {
	// Team yaml omits the `llm:` block entirely; the backend role also
	// omits per-role llm — so no model can be resolved.
	fixture := &hostFixture{
		yaml: `version: 1
container:
  image: ghcr.io/acme/dev:1.2.3
tools:
  all: ["*"]
`,
		roles: map[string]string{
			"backend": agentMD("triggers:\n  issue.opened: {}\npermission: read\ntools: [all]", "hi"),
		},
	}
	h := newTestSpawner(t, fixture, nil)
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
	fixture := &hostFixture{
		yaml: teamYAML, // team default model: claude-sonnet-4-6
		roles: map[string]string{
			"backend": agentMD(
				"triggers:\n  issue.opened: {}\npermission: read\ntools: [all]\nllm:\n  model: claude-opus-4-7",
				"hi"),
		},
	}
	h := newTestSpawner(t, fixture, nil)
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
	h := newTestSpawner(t, &hostFixture{yaml: "not: valid: yaml:::"}, nil)
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
	fm := "triggers:\n  issue.opened: {}\npermission: read\ntools: [all]"
	fixture := &hostFixture{
		yaml: teamYAML,
		roles: map[string]string{
			"backend":    agentMD(fm, "hi"),
			"dispatcher": agentMD(fm, "hi"),
			"reviewer":   agentMD(fm, "hi"),
		},
	}
	h := newTestSpawner(t, fixture, nil)
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
