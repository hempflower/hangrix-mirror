package service_test

import (
	"errors"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agents_config/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agents_config/service"
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
    agent: hangrix/dispatcher@v1.2.0
    triggers: [issue.opened, issue.comment.any]
    can: [issue_read, issue_comment, roster_list]

  backend:
    agent: acme/backend-coder@v0.3.1
    triggers: [issue.comment.mentioned]
    scope: { paths: ["apps/api/**", "internal/**"] }
    can:
      - issue_read
      - issue_diff
      - issue_comment
      - read
      - write
    mention_by: collaborators
    prompt: |
      Always git pull --rebase before push.

  reviewer:
    agent: hangrix/reviewer@v1.0.0
    triggers: [commit.pushed, issue.comment.mentioned]
    can: [issue_read, issue_diff, issue_comment]
    mention_by: collaborators
    prompt_file: .hangrix/prompts/reviewer.md
    llm:
      model: claude-opus-4-7
      max_tokens: 8000
      temperature: 0.2
      top_p: 0.9
`

func TestParseHostConfig_Happy(t *testing.T) {
	t.Parallel()

	cfg, err := service.ParseHostConfig([]byte(goldenHost))
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
	if disp.Agent.Owner != "hangrix" || disp.Agent.Name != "dispatcher" || disp.Agent.Ref != "v1.2.0" {
		t.Fatalf("dispatcher agent: %+v", disp.Agent)
	}
	if len(disp.Triggers) != 2 {
		t.Fatalf("dispatcher triggers: %+v", disp.Triggers)
	}

	rev := cfg.Roles["reviewer"]
	if rev.PromptFile != ".hangrix/prompts/reviewer.md" {
		t.Fatalf("reviewer prompt_file: %q", rev.PromptFile)
	}
	if rev.LLM == nil || rev.LLM.Model != "claude-opus-4-7" {
		t.Fatalf("reviewer llm: %+v", rev.LLM)
	}

	be := cfg.Roles["backend"]
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
    agent: a/b@v1
    triggers: [issue.opened]
`
	cfg, err := service.ParseHostConfig([]byte(body))
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
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidVersion,
		},
		{
			name: "missing-container",
			body: `
version: 1
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrContainerSourceConflict,
		},
		{
			name: "image-and-build",
			body: `
version: 1
container:
  image: x
  build:
    dockerfile: D
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrContainerSourceConflict,
		},
		{
			name: "neither-image-nor-build",
			body: `
version: 1
container: { env: { FOO: bar } }
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrContainerSourceConflict,
		},
		{
			name: "bad-env-key-lower",
			body: `
version: 1
container:
  image: x
  env: { node_env: 1 }
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidEnvKey,
		},
		{
			name: "bad-secret-name",
			body: `
version: 1
container:
  image: x
  secrets: [github_token]
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidSecretName,
		},
		{
			name: "bad-volume-mount-relative",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: cache, mount: caches/foo }
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidVolumeMount,
		},
		{
			name: "bad-volume-mount-escape",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: cache, mount: /caches/../../etc }
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidVolumeMount,
		},
		{
			name: "empty-volume-name",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: "", mount: /caches/x }
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidVolumeMount,
		},
		{
			name: "no-roles",
			body: `
version: 1
container: { image: x }
roles: {}
`,
			target: domain.ErrEmptyRoles,
		},
		{
			name: "bad-role-key",
			body: `
version: 1
container: { image: x }
roles: { Backend: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidRoleKey,
		},
		{
			name: "missing-agent-ref",
			body: `
version: 1
container: { image: x }
roles: { r: { agent: a/b, triggers: [issue.opened] } }
`,
			target: domain.ErrMissingAgentRef,
		},
		{
			name: "empty-triggers",
			body: `
version: 1
container: { image: x }
roles: { r: { agent: a/b@v1, triggers: [] } }
`,
			target: domain.ErrEmptyTriggers,
		},
		{
			name: "unknown-trigger",
			body: `
version: 1
container: { image: x }
roles: { r: { agent: a/b@v1, triggers: [issue.weird] } }
`,
			target: domain.ErrUnknownTrigger,
		},
		{
			name: "prompt-and-prompt-file",
			body: `
version: 1
container: { image: x }
roles:
  r:
    agent: a/b@v1
    triggers: [issue.opened]
    prompt: hi
    prompt_file: .hangrix/prompts/r.md
`,
			target: domain.ErrPromptMutuallyExclusive,
		},
		{
			name: "bad-prompt-file-prefix",
			body: `
version: 1
container: { image: x }
roles:
  r:
    agent: a/b@v1
    triggers: [issue.opened]
    prompt_file: prompts/r.md
`,
			target: domain.ErrInvalidPromptFilePath,
		},
		{
			name: "bad-prompt-file-escape",
			body: `
version: 1
container: { image: x }
roles:
  r:
    agent: a/b@v1
    triggers: [issue.opened]
    prompt_file: .hangrix/prompts/../../etc/x
`,
			target: domain.ErrInvalidPromptFilePath,
		},
		{
			name: "bad-mention-by",
			body: `
version: 1
container: { image: x }
roles:
  r:
    agent: a/b@v1
    triggers: [issue.opened]
    mention_by: maintainers
`,
			target: domain.ErrInvalidMentionBy,
		},
		{
			name: "bad-llm-temp",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  temperature: 5
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidLLMParam,
		},
		{
			name: "bad-llm-topp",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  top_p: 2
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidLLMParam,
		},
		{
			name: "bad-llm-maxtokens",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  max_tokens: -1
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidLLMParam,
		},
		{
			name: "llm-missing-model",
			body: `
version: 1
container: { image: x }
llm: { max_tokens: 100 }
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrInvalidModel,
		},
		{
			name: "per-role-llm-missing-model",
			body: `
version: 1
container: { image: x }
roles:
  r:
    agent: a/b@v1
    triggers: [issue.opened]
    llm: { max_tokens: 100 }
`,
			target: domain.ErrInvalidModel,
		},
		{
			name: "unknown-top-level",
			body: `
version: 1
container: { image: x }
weird: 1
roles: { r: { agent: a/b@v1, triggers: [issue.opened] } }
`,
			target: domain.ErrUnknownField,
		},
		{
			name: "unknown-role-field",
			body: `
version: 1
container: { image: x }
roles:
  r:
    agent: a/b@v1
    triggers: [issue.opened]
    weird: 1
`,
			target: domain.ErrUnknownField,
		},
		{
			name: "duplicate-role-key",
			body: "version: 1\ncontainer: { image: x }\nroles:\n  r:\n    agent: a/b@v1\n    triggers: [issue.opened]\n  r:\n    agent: a/c@v1\n    triggers: [issue.opened]\n",
			target: domain.ErrDuplicateRoleKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.ParseHostConfig([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected err, got nil")
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("got %v, want errors.Is %v", err, tc.target)
			}
		})
	}
}

func TestNormalizeHostConfig_FillsMentionByDefault(t *testing.T) {
	t.Parallel()

	body := `
version: 1
container: { image: x }
roles:
  explicit:
    agent: a/b@v1
    triggers: [issue.opened]
    mention_by: owner
  implicit:
    agent: a/c@v1
    triggers: [issue.opened]
`
	cfg, err := service.ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Roles["explicit"].MentionBy != domain.MentionByOwner {
		t.Fatalf("explicit: %q", cfg.Roles["explicit"].MentionBy)
	}
	// Before normalize: implicit is empty.
	if cfg.Roles["implicit"].MentionBy != "" {
		t.Fatalf("implicit pre-normalize: %q", cfg.Roles["implicit"].MentionBy)
	}

	service.NormalizeHostConfig(cfg)
	if cfg.Roles["explicit"].MentionBy != domain.MentionByOwner {
		t.Fatalf("explicit post-normalize changed: %q", cfg.Roles["explicit"].MentionBy)
	}
	if cfg.Roles["implicit"].MentionBy != domain.MentionByCollaborators {
		t.Fatalf("implicit default: %q", cfg.Roles["implicit"].MentionBy)
	}

	// Idempotent.
	service.NormalizeHostConfig(cfg)
	if cfg.Roles["implicit"].MentionBy != domain.MentionByCollaborators {
		t.Fatalf("not idempotent: %q", cfg.Roles["implicit"].MentionBy)
	}
}
