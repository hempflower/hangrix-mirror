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
  secrets:
    - GITHUB_TOKEN
    - NPM_AUTH_TOKEN
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
	if len(cfg.Container.Secrets) != 2 {
		t.Fatalf("secrets: %+v", cfg.Container.Secrets)
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

	rev := cfg.Roles["reviewer"]
	if rev.PromptFile != ".hangrix/prompts/reviewer.md" {
		t.Fatalf("reviewer prompt_file: %q", rev.PromptFile)
	}
	if rev.LLM == nil || rev.LLM.Model != "claude-opus-4-7" {
		t.Fatalf("reviewer llm: %+v", rev.LLM)
	}
	if rev.LLM.MaxOutputTokens != 8000 {
		t.Fatalf("reviewer llm max_output_tokens: %d", rev.LLM.MaxOutputTokens)
	}
	if rev.LLM.MaxContextTokens != 200000 {
		t.Fatalf("reviewer llm max_context_tokens: %d", rev.LLM.MaxContextTokens)
	}
	if rev.LLM.ReasoningEffort != "high" {
		t.Fatalf("reviewer llm reasoning_effort: %q", rev.LLM.ReasoningEffort)
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
			name: "bad-secret-name",
			body: `
version: 1
container:
  image: x
  secrets: [github_token]
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrInvalidSecretName,
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
			name: "comment-trigger-unknown-key",
			body: `
version: 1
container: { image: x }
roles:
  r:
    triggers:
      issue.comment:
        mention_only: true
    prompt: hi
`,
			target: ErrInvalidTriggerSpec,
		},
		{
			name: "push-trigger-unknown-key",
			body: `
version: 1
container: { image: x }
roles:
  r:
    triggers:
      commit.pushed:
        paths-include: ["apps/**"]
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
			name: "bad-llm-reasoning-effort",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  reasoning_effort: ultra
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
			name: "per-role-llm-missing-model",
			body: `
version: 1
container: { image: x }
roles:
  r:
    triggers:
      issue.opened: {}
    prompt: hi
    llm: { max_output_tokens: 100 }
`,
			target: ErrInvalidModel,
		},
		{
			name: "unknown-top-level",
			body: `
version: 1
container: { image: x }
weird: 1
roles: { r: { triggers: { issue.opened: {} }, prompt: hi } }
`,
			target: ErrUnknownField,
		},
		{
			name: "unknown-role-field",
			body: `
version: 1
container: { image: x }
roles:
  r:
    triggers:
      issue.opened: {}
    prompt: hi
    weird: 1
`,
			target: ErrUnknownField,
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

