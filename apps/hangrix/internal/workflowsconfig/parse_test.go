package workflowsconfig

import (
	"testing"
)

func TestParseWorkflowConfig_MinimalValid(t *testing.T) {
	raw := []byte(`
version: 1
name: ci
on:
  repo.push: {}
jobs:
  build:
    steps:
      - run: echo hello
`)
	cfg, err := ParseWorkflowConfig(raw, "ci.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "ci" {
		t.Errorf("name = %q, want %q", cfg.Name, "ci")
	}
	if cfg.SourceFile != "ci.yml" {
		t.Errorf("sourceFile = %q, want %q", cfg.SourceFile, "ci.yml")
	}
	if len(cfg.On) != 1 {
		t.Fatalf("got %d triggers, want 1", len(cfg.On))
	}
	if cfg.On[0].Event != EventRepoPush {
		t.Errorf("event = %q, want %q", cfg.On[0].Event, EventRepoPush)
	}
	if len(cfg.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(cfg.Jobs))
	}
	if cfg.Jobs[0].Key != "build" {
		t.Errorf("job key = %q, want %q", cfg.Jobs[0].Key, "build")
	}
	if cfg.Jobs[0].DisplayName != "build" {
		t.Errorf("job display name = %q, want %q (default = key)", cfg.Jobs[0].DisplayName, "build")
	}
	if cfg.Jobs[0].TimeoutMinutes != 60 {
		t.Errorf("timeout_minutes = %d, want 60 (default)", cfg.Jobs[0].TimeoutMinutes)
	}
	if cfg.Jobs[0].WorkingDirectory != "/workspace" {
		t.Errorf("working_directory = %q, want /workspace (default)", cfg.Jobs[0].WorkingDirectory)
	}
	if len(cfg.Jobs[0].Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(cfg.Jobs[0].Steps))
	}
	if cfg.Jobs[0].Steps[0].Run != "echo hello" {
		t.Errorf("step run = %q, want %q", cfg.Jobs[0].Steps[0].Run, "echo hello")
	}
}

func TestParseWorkflowConfig_MissingName(t *testing.T) {
	raw := []byte(`
on:
  repo.push: {}
jobs:
  build:
    steps:
      - run: echo hello
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParseWorkflowConfig_InvalidName(t *testing.T) {
	raw := []byte(`
name: CI-CD!
on:
  repo.push: {}
jobs:
  build:
    steps:
      - run: echo hello
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParseWorkflowConfig_MissingOn(t *testing.T) {
	raw := []byte(`
name: ci
on: {}
jobs:
  build:
    steps:
      - run: echo hello
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for empty on, got nil")
	}
}

func TestParseWorkflowConfig_UnknownEvent(t *testing.T) {
	raw := []byte(`
name: ci
on:
  unknown.event: {}
jobs:
  build:
    steps:
      - run: echo hello
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for unknown event, got nil")
	}
}

func TestParseWorkflowConfig_MissingJobs(t *testing.T) {
	raw := []byte(`
name: ci
on:
  repo.push: {}
jobs: {}
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for empty jobs, got nil")
	}
}

func TestParseWorkflowConfig_JobMissingSteps(t *testing.T) {
	raw := []byte(`
name: ci
on:
  repo.push: {}
jobs:
  build:
    name: Build
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for job missing steps, got nil")
	}
}

func TestParseWorkflowConfig_StepMissingRun(t *testing.T) {
	raw := []byte(`
name: ci
on:
  repo.push: {}
jobs:
  build:
    steps:
      - name: check
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for step missing run, got nil")
	}
}

func TestParseWorkflowConfig_InvalidJobKey(t *testing.T) {
	raw := []byte(`
name: ci
on:
  repo.push: {}
jobs:
  Build Job:
    steps:
      - run: echo hello
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for invalid job key, got nil")
	}
}

func TestParseWorkflowConfig_WorkflowDispatch(t *testing.T) {
	raw := []byte(`
version: 1
name: deploy
on:
  workflow.dispatch:
    inputs:
      - name: env
        required: true
      - name: tag
jobs:
  deploy:
    steps:
      - run: deploy
`)
	cfg, err := ParseWorkflowConfig(raw, "deploy.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.DispatchInputs) != 2 {
		t.Fatalf("got %d dispatch inputs, want 2", len(cfg.DispatchInputs))
	}
	if cfg.DispatchInputs[0].Name != "env" {
		t.Errorf("dispatch input[0].name = %q, want %q", cfg.DispatchInputs[0].Name, "env")
	}
	if !cfg.DispatchInputs[0].Required {
		t.Errorf("dispatch input[0].required = false, want true")
	}
	if cfg.DispatchInputs[1].Name != "tag" {
		t.Errorf("dispatch input[1].name = %q, want %q", cfg.DispatchInputs[1].Name, "tag")
	}
}

func TestParseWorkflowConfig_IssueCommentWithFilters(t *testing.T) {
	raw := []byte(`
version: 1
name: triage
on:
  issue.comment:
    mentioned_only: true
    from_roles:
      - maintainer
    from_users:
      - alice
jobs:
  triage:
    steps:
      - run: echo triage
`)
	cfg, err := ParseWorkflowConfig(raw, "triage.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.On) != 1 {
		t.Fatalf("got %d triggers, want 1", len(cfg.On))
	}
	trig := cfg.On[0]
	if trig.Event != EventIssueComment {
		t.Errorf("event = %q, want %q", trig.Event, EventIssueComment)
	}
	if !trig.MentionedOnly {
		t.Error("mentioned_only = false, want true")
	}
	if len(trig.FromRoles) != 1 || trig.FromRoles[0] != "maintainer" {
		t.Errorf("from_roles = %v, want [maintainer]", trig.FromRoles)
	}
	if len(trig.FromUsers) != 1 || trig.FromUsers[0] != "alice" {
		t.Errorf("from_users = %v, want [alice]", trig.FromUsers)
	}
}

func TestParseWorkflowConfig_InvalidEnvKey(t *testing.T) {
	raw := []byte(`
name: ci
on:
  repo.push: {}
env:
  lower-case: val
jobs:
  build:
    steps:
      - run: echo hello
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for invalid env key, got nil")
	}
}

func TestParseWorkflowConfig_BadVersion(t *testing.T) {
	raw := []byte(`
version: 2
name: ci
on:
  repo.push: {}
jobs:
  build:
    steps:
      - run: echo hello
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for bad version, got nil")
	}
}

func TestValidateConfigSet_DuplicateNames(t *testing.T) {
	configs := []*WorkflowConfig{
		{Name: "ci", SourceFile: "ci.yml"},
		{Name: "ci", SourceFile: "other.yml"},
	}
	err := ValidateConfigSet(configs)
	if err == nil {
		t.Fatal("expected error for duplicate names, got nil")
	}
}

func TestValidateConfigSet_OK(t *testing.T) {
	configs := []*WorkflowConfig{
		{Name: "ci", SourceFile: "ci.yml"},
		{Name: "deploy", SourceFile: "deploy.yml"},
	}
	err := ValidateConfigSet(configs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMatchesPushEvent_ExactBranch(t *testing.T) {
	trig := EventTrigger{
		Event:    EventRepoPush,
		Branches: []string{"main"},
	}
	if !trig.MatchesPushEvent("main", nil) {
		t.Error("expected match for branch main")
	}
	if trig.MatchesPushEvent("develop", nil) {
		t.Error("expected no match for branch develop")
	}
}

func TestMatchesPushEvent_BranchGlob(t *testing.T) {
	trig := EventTrigger{
		Event:    EventRepoPush,
		Branches: []string{"feature/*"},
	}
	if !trig.MatchesPushEvent("feature/foo", nil) {
		t.Error("expected match for branch feature/foo")
	}
	if trig.MatchesPushEvent("main", nil) {
		t.Error("expected no match for branch main")
	}
}

func TestMatchesPushEvent_PathFilter(t *testing.T) {
	trig := EventTrigger{
		Event: EventRepoPush,
		Paths: []string{"apps/**"},
	}
	if !trig.MatchesPushEvent("main", []string{"apps/web/foo.go"}) {
		t.Error("expected match for apps/web/foo.go")
	}
	if trig.MatchesPushEvent("main", []string{"docs/readme.md"}) {
		t.Error("expected no match for docs/readme.md")
	}
}

func TestMatchesCommentEvent_FromRoles(t *testing.T) {
	trig := EventTrigger{
		Event:     EventIssueComment,
		FromRoles: []string{"maintainer"},
	}
	if !trig.MatchesCommentEvent("maintainer", "", "") {
		t.Error("expected match for role maintainer")
	}
	if trig.MatchesCommentEvent("contributor", "", "") {
		t.Error("expected no match for role contributor")
	}
}

func TestMatchesCommentEvent_MentionedOnly(t *testing.T) {
	trig := EventTrigger{
		Event:         EventIssueComment,
		MentionedOnly: true,
	}
	// When mentioned_only is true and no workflow mention is detected:
	if trig.MatchesCommentEvent("", "", "") {
		t.Error("expected no match when mentioned_only and no mention")
	}
}
