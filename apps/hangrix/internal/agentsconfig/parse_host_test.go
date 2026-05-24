package agentsconfig

import (
	"errors"
	"testing"
)

const goldenHost = `
version: 1

container:
  image: ghcr.io/acme/dev:1.2.3
  env:
    NODE_ENV: development
    GOFLAGS: "-mod=readonly"
  volumes:
    - { name: pnpm-store, mount: /caches/pnpm }
    - { name: go-mod,    mount: /go/pkg/mod }

llm:
  model: claude-sonnet-4-6

roles:
  dispatcher:
    triggers:
      issue.opened: {}
      issue.comment: {}
    can: [issue_read, issue_comment, roster_list]
    prompt: |
      You are the dispatcher.

  backend:
    triggers:
      issue.comment:
        mentioned_only: true
        from_users: [alice, bob]
    scope: { paths: ["apps/api/**", "internal/**"] }
    mcp: [playwright, playwright]   # duplicate intentionally tests dedup
    can:
      - issue_read
      - issue_diff
      - issue_comment
      - read
      - write
    prompt: |
      Always git pull --rebase before push.

  reviewer:
    triggers:
      commit.pushed:
        paths: ["apps/api/**", "internal/**"]
        paths_ignore: ["**/*.md"]
      issue.comment:
        mentioned_only: true
        from_roles: [dispatcher]
    can: [issue_read, issue_diff, issue_comment]
    prompt_file: .hangrix/prompts/reviewer.md
    llm:
      model: claude-opus-4-7
      max_output_tokens: 8000
      max_context_tokens: 200000
      reasoning_effort: high
      temperature: 0.2
      top_p: 0.9
`

func TestParseHostConfig_Happy(t *testing.T) {
	t.Parallel()

	cfg, err := ParseHostConfig([]byte(goldenHost))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version: %d", cfg.Version)
	}
	if cfg.Container.Image != "ghcr.io/acme/dev:1.2.3" {
		t.Fatalf("image: %q", cfg.Container.Image)
	}
	if cfg.Container.Build != nil {
		t.Fatalf("expected no build, got %+v", cfg.Container.Build)
	}
	if cfg.Container.Env["NODE_ENV"] != "development" {
		t.Fatalf("env NODE_ENV: %+v", cfg.Container.Env)
	}
	if len(cfg.Container.Volumes) != 2 || cfg.Container.Volumes[0].Mount != "/caches/pnpm" {
		t.Fatalf("volumes: %+v", cfg.Container.Volumes)
	}
	if cfg.LLM == nil || cfg.LLM.Model != "claude-sonnet-4-6" {
		t.Fatalf("team llm: %+v", cfg.LLM)
	}
	if len(cfg.Roles) != 3 {
		t.Fatalf("roles count: %d", len(cfg.Roles))
	}

	disp := cfg.Roles["dispatcher"]
	if disp == nil {
		t.Fatalf("dispatcher missing")
	}
	if disp.Prompt == "" {
		t.Fatalf("dispatcher prompt empty")
	}
	if len(disp.Triggers) != 2 {
		t.Fatalf("dispatcher triggers: %+v", disp.Triggers)
	}
	if _, ok := disp.Triggers[TriggerIssueOpened]; !ok {
		t.Fatalf("dispatcher missing issue.opened trigger")
	}
	if cs := disp.Triggers[TriggerIssueComment]; cs == nil || cs.Comment == nil || cs.Comment.MentionedOnly {
		t.Fatalf("dispatcher issue.comment should be no-filter: %+v", cs)
	}

	be := cfg.Roles["backend"]
	beCmt := be.Triggers[TriggerIssueComment]
	if beCmt == nil || beCmt.Comment == nil || !beCmt.Comment.MentionedOnly {
		t.Fatalf("backend issue.comment: expected mentioned_only=true, got %+v", beCmt)
	}
	if want := []string{"alice", "bob"}; len(beCmt.Comment.FromUsers) != 2 || beCmt.Comment.FromUsers[0] != want[0] {
		t.Fatalf("backend from_users: %+v", beCmt.Comment.FromUsers)
	}
	// MCP: duplicate "playwright" in yaml → deduped to single entry.
	if want := []string{"playwright"}; len(be.MCP) != 1 || be.MCP[0] != want[0] {
		t.Fatalf("backend mcp: %+v", be.MCP)
	}

	rev := cfg.Roles["reviewer"]
	if rev.PromptFile != ".hangrix/prompts/reviewer.md" {
		t.Fatalf("reviewer prompt_file: %q", rev.PromptFile)
	}
	if rev.LLM == nil || rev.LLM.Model != "claude-opus-4-7" {
		t.Fatalf("reviewer llm: %+v", rev.LLM)
	}
	if rev.LLM.MaxOutputTokens == nil || *rev.LLM.MaxOutputTokens != 8000 {
		t.Fatalf("reviewer llm max_output_tokens: %v", rev.LLM.MaxOutputTokens)
	}
	if rev.LLM.MaxContextTokens == nil || *rev.LLM.MaxContextTokens != 200000 {
		t.Fatalf("reviewer llm max_context_tokens: %v", rev.LLM.MaxContextTokens)
	}
	if rev.LLM.ReasoningEffort == nil || *rev.LLM.ReasoningEffort != "high" {
		t.Fatalf("reviewer llm reasoning_effort: %v", rev.LLM.ReasoningEffort)
	}
	revPush := rev.Triggers[TriggerCommitPushed]
	if revPush == nil || revPush.Push == nil {
		t.Fatalf("reviewer commit.pushed missing")
	}
	if len(revPush.Push.Paths) != 2 || revPush.Push.Paths[0] != "apps/api/**" {
		t.Fatalf("reviewer commit.pushed.paths: %+v", revPush.Push.Paths)
	}
	if len(revPush.Push.PathsIgnore) != 1 || revPush.Push.PathsIgnore[0] != "**/*.md" {
		t.Fatalf("reviewer commit.pushed.paths_ignore: %+v", revPush.Push.PathsIgnore)
	}
	revCmt := rev.Triggers[TriggerIssueComment]
	if revCmt == nil || revCmt.Comment == nil || !revCmt.Comment.MentionedOnly {
		t.Fatalf("reviewer issue.comment.mentioned_only: %+v", revCmt)
	}
	if len(revCmt.Comment.FromRoles) != 1 || revCmt.Comment.FromRoles[0] != "dispatcher" {
		t.Fatalf("reviewer issue.comment.from_roles: %+v", revCmt.Comment.FromRoles)
	}

	if len(be.Scope.Paths) != 2 || be.Scope.Paths[0] != "apps/api/**" {
		t.Fatalf("backend scope: %+v", be.Scope)
	}
}

func TestParseHostConfig_Entrypoint(t *testing.T) {
	t.Parallel()

	body := `
version: 1
container:
  image: x
  entrypoint: ["/init"]
roles:
  r:
    triggers: { issue.opened: {} }
    prompt: hi
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got, want := cfg.Container.Entrypoint, []string{"/init"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("entrypoint: %v", got)
	}
}

func TestParseHostConfig_EntrypointOmittedDefaultsToNil(t *testing.T) {
	t.Parallel()

	body := `
version: 1
container: { image: x }
roles:
  r:
    triggers: { issue.opened: {} }
    prompt: hi
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Container.Entrypoint != nil {
		t.Fatalf("entrypoint should be nil when omitted, got %v", cfg.Container.Entrypoint)
	}
}

func TestParseHostConfig_Build(t *testing.T) {
	t.Parallel()

	body := `
version: 1
container:
  build:
    dockerfile: .hangrix/agent.Dockerfile
    context: .
    args: { GO_VERSION: "1.26" }
roles:
  only:
    triggers:
      issue.opened: {}
    prompt: hi
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Container.Build == nil || cfg.Container.Build.Dockerfile != ".hangrix/agent.Dockerfile" {
		t.Fatalf("build: %+v", cfg.Container.Build)
	}
	if cfg.Container.Build.Args["GO_VERSION"] != "1.26" {
		t.Fatalf("build args: %+v", cfg.Container.Build.Args)
	}
}

func TestParseHostConfig_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		body   string
		target error
	}{
		{
			name: "bad-version",
			body: `
version: 2
container: { image: x }
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidVersion,
		},
		{
			name: "missing-container",
			body: `
version: 1
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrContainerSourceConflict,
		},
		{
			name: "image-and-build",
			body: `
version: 1
container:
  image: x
  build:
    dockerfile: D
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrContainerSourceConflict,
		},
		{
			name: "neither-image-nor-build",
			body: `
version: 1
container: { env: { FOO: bar } }
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrContainerSourceConflict,
		},
		{
			name: "bad-env-key-lower",
			body: `
version: 1
container:
  image: x
  env: { node_env: 1 }
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidEnvKey,
		},
		{
			name: "bad-volume-mount-relative",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: cache, mount: caches/foo }
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidVolumeMount,
		},
		{
			name: "bad-volume-mount-escape",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: cache, mount: /caches/../../etc }
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidVolumeMount,
		},
		{
			name: "empty-volume-name",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: "", mount: /caches/x }
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidVolumeMount,
		},
		{
			name: "no-roles",
			body: `
version: 1
container: { image: x }
roles: {}
`,
			target: ErrEmptyRoles,
		},
		{
			name: "bad-role-key",
			body: `
version: 1
container: { image: x }
roles: { Backend: { triggers: [issue.opened], prompt: hi } }
`,
			target: ErrInvalidRoleKey,
		},
		{
			name: "missing-prompt",
			body: `
version: 1
container: { image: x }
roles: { r: { triggers: { issue.opened: {} } } }
`,
			target: ErrPromptMissing,
		},
		{
			name: "empty-triggers-map",
			body: `
version: 1
container: { image: x }
roles: { r: { triggers: {}, prompt: hi } }
`,
			target: ErrEmptyTriggers,
		},
		{
			name: "missing-triggers",
			body: `
version: 1
container: { image: x }
roles: { r: { prompt: hi } }
`,
			target: ErrEmptyTriggers,
		},
		{
			name: "triggers-not-mapping",
			body: `
version: 1
container: { image: x }
roles: { r: { triggers: [issue.opened], prompt: hi } }
`,
			target: ErrInvalidTriggerSpec,
		},
		{
			name: "unknown-trigger",
			body: `
version: 1
container: { image: x }
roles: { r: { triggers: { issue.weird: {} }, prompt: hi } }
`,
			target: ErrUnknownTrigger,
		},
		{
			name: "filterless-trigger-with-filter",
			body: `
version: 1
container: { image: x }
roles:
  r:
    triggers:
      issue.opened:
        paths: ["x/**"]
    prompt: hi
`,
			target: ErrInvalidTriggerSpec,
		},
		{
			name: "prompt-and-prompt-file",
			body: `
version: 1
container: { image: x }
roles:
  r:
    triggers:
      issue.opened: {}
    prompt: hi
    prompt_file: .hangrix/prompts/r.md
`,
			target: ErrPromptMutuallyExclusive,
		},
		{
			name: "bad-prompt-file-prefix",
			body: `
version: 1
container: { image: x }
roles:
  r:
    triggers:
      issue.opened: {}
    prompt_file: prompts/r.md
`,
			target: ErrInvalidPromptFilePath,
		},
		{
			name: "bad-prompt-file-escape",
			body: `
version: 1
container: { image: x }
roles:
  r:
    triggers:
      issue.opened: {}
    prompt_file: .hangrix/prompts/../../etc/x
`,
			target: ErrInvalidPromptFilePath,
		},
		{
			name: "bad-llm-temp",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  temperature: 5
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidLLMParam,
		},
		{
			name: "bad-llm-topp",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  top_p: 2
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidLLMParam,
		},
		{
			name: "bad-llm-max-output-tokens",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  max_output_tokens: -1
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidLLMParam,
		},
		{
			name: "bad-llm-max-context-tokens",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  max_context_tokens: -1
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidLLMParam,
		},
		{
			name: "llm-missing-model",
			body: `
version: 1
container: { image: x }
llm: { max_output_tokens: 100 }
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidModel,
		},
		{
			name: "entrypoint-empty-element",
			body: `
version: 1
container:
  image: x
  entrypoint: ["/init", ""]
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidContainerEntrypoint,
		},
		{
			name: "mcp-empty-server-name",
			body: `
version: 1
container: { image: x }
roles: { r: { triggers: { issue.opened: {} }, prompt: hi, mcp: ["", playwright] } }
`,
			target: ErrInvalidMCP,
		},
		{
			name: "duplicate-role-key",
			body: "version: 1\ncontainer: { image: x }\nroles:\n  r:\n    triggers: { issue.opened: {} }\n    prompt: hi\n  r:\n    triggers: { issue.opened: {} }\n    prompt: hi\n",
			target: ErrDuplicateRoleKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseHostConfig([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected err, got nil")
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("got %v, want errors.Is %v", err, tc.target)
			}
		})
	}
}

// TestParseHostConfig_RoleLLMInheritsFields pins the field-level
// inheritance contract: a role's `llm:` block MAY omit any subset of
// fields (including `model`); omitted fields inherit the team default,
// present fields override it — including legitimate zero values like
// `temperature: 0`. Inheritance itself is applied by the spawner; the
// parser's job is just to preserve "omitted vs explicitly zero" so the
// merge has the information it needs.
func TestParseHostConfig_RoleLLMInheritsFields(t *testing.T) {
	t.Parallel()

	body := `
version: 1
container: { image: x }
llm:
  model: team-model
  max_output_tokens: 4096
  max_context_tokens: 100000
  reasoning_effort: medium
  temperature: 0.7
roles:
  bumps-effort-only:
    triggers: { issue.opened: {} }
    prompt: hi
    llm:
      reasoning_effort: high
  resets-temperature-to-zero:
    triggers: { issue.opened: {} }
    prompt: hi
    llm:
      temperature: 0
  swaps-model-only:
    triggers: { issue.opened: {} }
    prompt: hi
    llm:
      model: special-model
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	r1 := cfg.Roles["bumps-effort-only"]
	if r1.LLM == nil {
		t.Fatalf("bumps-effort-only.LLM nil")
	}
	if r1.LLM.Model != "" {
		t.Fatalf("model should be empty (inherit): %q", r1.LLM.Model)
	}
	if r1.LLM.ReasoningEffort == nil || *r1.LLM.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort: %v", r1.LLM.ReasoningEffort)
	}
	if r1.LLM.MaxOutputTokens != nil {
		t.Fatalf("max_output_tokens should be nil (inherit): %v", *r1.LLM.MaxOutputTokens)
	}
	if r1.LLM.Temperature != nil {
		t.Fatalf("temperature should be nil (inherit): %v", *r1.LLM.Temperature)
	}

	r2 := cfg.Roles["resets-temperature-to-zero"]
	if r2.LLM == nil || r2.LLM.Temperature == nil {
		t.Fatalf("temperature should be non-nil pointer-to-zero, got %+v", r2.LLM)
	}
	if *r2.LLM.Temperature != 0 {
		t.Fatalf("temperature: %v", *r2.LLM.Temperature)
	}

	r3 := cfg.Roles["swaps-model-only"]
	if r3.LLM == nil || r3.LLM.Model != "special-model" {
		t.Fatalf("model: %+v", r3.LLM)
	}
	if r3.LLM.MaxOutputTokens != nil || r3.LLM.ReasoningEffort != nil {
		t.Fatalf("other fields should be nil (inherit): %+v", r3.LLM)
	}
}

// TestParseHostConfig_IgnoresUnknownFields pins the forward-compat
// contract: unknown keys at the top level, inside a role body, and
// inside a per-trigger filter block are all silently dropped rather
// than rejected. A typo-by-the-author still fails *behaviourally* (the
// filter ends up empty), but parsing succeeds so a host shipping
// newer fields against an older agent server doesn't brick.
func TestParseHostConfig_IgnoresUnknownFields(t *testing.T) {
	t.Parallel()

	body := `
version: 1
weird_top_level: 42
container:
  image: x
  weird_container_field: ignore-me
roles:
  r:
    triggers:
      issue.opened: {}
      issue.comment:
        mention_only: true        # typo for mentioned_only, silently dropped
      commit.pushed:
        paths-include: ["apps/**"] # typo for paths, silently dropped
    weird_role_field: 1
    prompt: hi
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unknown fields should be ignored, got err: %v", err)
	}
	role := cfg.Roles["r"]
	if role == nil {
		t.Fatalf("role r missing")
	}
	cmt := role.Triggers[TriggerIssueComment]
	if cmt == nil || cmt.Comment == nil || cmt.Comment.MentionedOnly {
		t.Fatalf("typo'd mention_only should leave filter empty: %+v", cmt)
	}
	push := role.Triggers[TriggerCommitPushed]
	if push == nil || push.Push == nil || len(push.Push.Paths) != 0 {
		t.Fatalf("typo'd paths-include should leave filter empty: %+v", push)
	}
}

