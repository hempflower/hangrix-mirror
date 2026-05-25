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

func TestParseWorkflowConfig_RepoPushTag(t *testing.T) {
	raw := []byte(`
version: 1
name: release
on:
  repo.push_tag:
    tags: ["v*", "release-*"]
    tags_ignore: ["*-rc*"]
jobs:
  build:
    steps:
      - run: echo release
`)
	cfg, err := ParseWorkflowConfig(raw, "release.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.On) != 1 {
		t.Fatalf("got %d triggers, want 1", len(cfg.On))
	}
	trig := cfg.On[0]
	if trig.Event != EventRepoPushTag {
		t.Errorf("event = %q, want %q", trig.Event, EventRepoPushTag)
	}
	if len(trig.Tags) != 2 || trig.Tags[0] != "v*" || trig.Tags[1] != "release-*" {
		t.Errorf("tags = %v, want [v* release-*]", trig.Tags)
	}
	if len(trig.TagsIgnore) != 1 || trig.TagsIgnore[0] != "*-rc*" {
		t.Errorf("tags_ignore = %v, want [*-rc*]", trig.TagsIgnore)
	}
}

func TestParseWorkflowConfig_RepoPushTagExtraKeyIgnored(t *testing.T) {
	// Lenient parsing: an unknown sub-key under a trigger is ignored, not
	// rejected. The known keys still apply.
	raw := []byte(`
version: 1
name: release
on:
  repo.push_tag:
    tags: ["v*"]
    branches: [main]
jobs:
  build:
    steps:
      - run: echo release
`)
	cfg, err := ParseWorkflowConfig(raw, "release.yml")
	if err != nil {
		t.Fatalf("unexpected error (unknown key should be ignored): %v", err)
	}
	if len(cfg.On[0].Tags) != 1 || cfg.On[0].Tags[0] != "v*" {
		t.Errorf("tags = %v, want [v*]", cfg.On[0].Tags)
	}
}

func TestParseWorkflowConfig_RepoPushTagEmptyIsValid(t *testing.T) {
	raw := []byte(`
version: 1
name: release
on:
  repo.push_tag: {}
jobs:
  build:
    steps:
      - run: echo release
`)
	cfg, err := ParseWorkflowConfig(raw, "release.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.On[0].Event != EventRepoPushTag {
		t.Errorf("event = %q, want %q", cfg.On[0].Event, EventRepoPushTag)
	}
}

func TestMatchesPushTagEvent_ExactTag(t *testing.T) {
	trig := EventTrigger{
		Event: EventRepoPushTag,
		Tags:  []string{"v1.0.0"},
	}
	if !trig.MatchesPushTagEvent("v1.0.0") {
		t.Error("expected match for tag v1.0.0")
	}
	if trig.MatchesPushTagEvent("v2.0.0") {
		t.Error("expected no match for tag v2.0.0")
	}
}

func TestMatchesPushTagEvent_Glob(t *testing.T) {
	trig := EventTrigger{
		Event: EventRepoPushTag,
		Tags:  []string{"v*"},
	}
	if !trig.MatchesPushTagEvent("v1.2.3") {
		t.Error("expected match for tag v1.2.3 with pattern v*")
	}
	if !trig.MatchesPushTagEvent("v0.0.1") {
		t.Error("expected match for tag v0.0.1 with pattern v*")
	}
	if trig.MatchesPushTagEvent("release-1") {
		t.Error("expected no match for tag release-1 with pattern v*")
	}
}

func TestMatchesPushTagEvent_TagsIgnore(t *testing.T) {
	trig := EventTrigger{
		Event:      EventRepoPushTag,
		Tags:       []string{"v*"},
		TagsIgnore: []string{"*-rc*"},
	}
	if !trig.MatchesPushTagEvent("v1.0.0") {
		t.Error("expected match for tag v1.0.0")
	}
	if trig.MatchesPushTagEvent("v1.0.0-rc1") {
		t.Error("expected no match for tag v1.0.0-rc1 (matches tags_ignore)")
	}
}

func TestMatchesPushTagEvent_WrongEvent(t *testing.T) {
	trig := EventTrigger{
		Event: EventRepoPush,
		Tags:  []string{"v*"},
	}
	if trig.MatchesPushTagEvent("v1.0.0") {
		t.Error("expected no match when event is repo.push, not repo.push_tag")
	}
}

func TestParseWorkflowConfig_ReleaseStep(t *testing.T) {
	raw := []byte(`
version: 1
name: release
on:
  repo.push_tag:
    tags: ["v*"]
jobs:
  publish:
    steps:
      - id: create-release
        type: release
        with:
          tag: v1.0.0
          notes: |
            Release for v1.0.0
          draft: false
`)
	cfg, err := ParseWorkflowConfig(raw, "release.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(cfg.Jobs))
	}
	steps := cfg.Jobs[0].Steps
	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(steps))
	}
	s := steps[0]
	if s.Type != StepTypeRelease {
		t.Errorf("type = %q, want %q", s.Type, StepTypeRelease)
	}
	// Release params live verbatim under With; the runner interprets them.
	if got := s.With["tag"]; got != "v1.0.0" {
		t.Errorf("with.tag = %v, want %q", got, "v1.0.0")
	}
	if got := s.With["notes"]; got != "Release for v1.0.0\n" {
		t.Errorf("with.notes = %v, want %q", got, "Release for v1.0.0\n")
	}
	if got := s.With["draft"]; got != false {
		t.Errorf("with.draft = %v, want false", got)
	}
	if s.Run != "" {
		t.Errorf("run = %q, want empty", s.Run)
	}
}

func TestParseWorkflowConfig_ReleaseStepMissingTag(t *testing.T) {
	// A release step still requires with.tag — that's a structural
	// requirement, not an "unknown field".
	raw := []byte(`
version: 1
name: release
on:
  repo.push_tag: {}
jobs:
  publish:
    steps:
      - type: release
        with:
          notes: no tag here
`)
	_, err := ParseWorkflowConfig(raw, "release.yml")
	if err == nil {
		t.Fatal("expected error for release step missing with.tag, got nil")
	}
}

func TestParseWorkflowConfig_ReleaseStepWithRunIsLenient(t *testing.T) {
	// Lenient: an irrelevant `run` on a release step is ignored, not rejected.
	raw := []byte(`
version: 1
name: release
on:
  repo.push_tag: {}
jobs:
  publish:
    steps:
      - type: release
        run: echo ignored
        with:
          tag: v1.0.0
`)
	if _, err := ParseWorkflowConfig(raw, "release.yml"); err != nil {
		t.Fatalf("unexpected error (irrelevant run should be ignored): %v", err)
	}
}

func TestParseWorkflowConfig_RunStepExtraKeyIsLenient(t *testing.T) {
	// Lenient: unknown top-level step keys (e.g. a stray `tag`) are ignored.
	raw := []byte(`
version: 1
name: ci
on:
  repo.push: {}
jobs:
  build:
    steps:
      - type: run
        run: echo hello
        tag: ignored
        unknown_key: also-ignored
`)
	if _, err := ParseWorkflowConfig(raw, "ci.yml"); err != nil {
		t.Fatalf("unexpected error (unknown step keys should be ignored): %v", err)
	}
}

func TestParseWorkflowConfig_UnknownStepType(t *testing.T) {
	raw := []byte(`
version: 1
name: ci
on:
  repo.push: {}
jobs:
  build:
    steps:
      - type: foobar
        run: echo hello
`)
	_, err := ParseWorkflowConfig(raw, "ci.yml")
	if err == nil {
		t.Fatal("expected error for unknown step type, got nil")
	}
}

func TestParseWorkflowConfig_MixedRunAndReleaseSteps(t *testing.T) {
	raw := []byte(`
version: 1
name: release
on:
  repo.push_tag:
    tags: ["v*"]
jobs:
  publish:
    steps:
      - id: build
        run: make release
      - id: create-release
        type: release
        with:
          tag: v1.0.0
          draft: false
`)
	cfg, err := ParseWorkflowConfig(raw, "release.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	steps := cfg.Jobs[0].Steps
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].Type != StepTypeRun {
		t.Errorf("step[0].type = %q, want %q", steps[0].Type, StepTypeRun)
	}
	if steps[0].Run != "make release" {
		t.Errorf("step[0].run = %q, want %q", steps[0].Run, "make release")
	}
	if steps[1].Type != StepTypeRelease {
		t.Errorf("step[1].type = %q, want %q", steps[1].Type, StepTypeRelease)
	}
	if got := steps[1].With["tag"]; got != "v1.0.0" {
		t.Errorf("step[1].with.tag = %v, want %q", got, "v1.0.0")
	}
}
