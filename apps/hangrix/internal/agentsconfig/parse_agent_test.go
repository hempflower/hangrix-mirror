package agentsconfig

import (
	"errors"
	"strings"
	"testing"

)

func TestParseAgentManifest_Happy(t *testing.T) {
	t.Parallel()

	body := []byte(`
version: 1
kind: agent
entry:
  base_prompt: prompts/system.md
declared_tools:
  - issue_read
  - issue_comment
  - issue_review_vote
`)

	got, err := ParseAgentManifest(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Version != 1 || got.Kind != "agent" {
		t.Fatalf("got %+v", got)
	}
	if got.Entry.BasePrompt != "prompts/system.md" {
		t.Fatalf("base_prompt: %q", got.Entry.BasePrompt)
	}
	if len(got.DeclaredTools) != 3 || got.DeclaredTools[0] != "issue_read" {
		t.Fatalf("declared_tools: %+v", got.DeclaredTools)
	}
}

func TestParseAgentManifest_HappyNoTools(t *testing.T) {
	t.Parallel()

	body := []byte(`
version: 1
kind: agent
entry:
  base_prompt: prompts/x.md
`)
	got, err := ParseAgentManifest(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got.DeclaredTools) != 0 {
		t.Fatalf("expected no declared tools, got %+v", got.DeclaredTools)
	}
}

func TestParseAgentManifest_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		body   string
		target error
	}{
		{
			name:   "bad-version",
			body:   "version: 2\nkind: agent\nentry:\n  base_prompt: p.md\n",
			target: ErrInvalidVersion,
		},
		{
			name:   "missing-version",
			body:   "kind: agent\nentry:\n  base_prompt: p.md\n",
			target: ErrInvalidVersion,
		},
		{
			name:   "bad-kind",
			body:   "version: 1\nkind: tool\nentry:\n  base_prompt: p.md\n",
			target: ErrInvalidKind,
		},
		{
			name:   "missing-base-prompt",
			body:   "version: 1\nkind: agent\nentry: {}\n",
			target: ErrMissingBasePrompt,
		},
		{
			name:   "absolute-base-prompt",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: /etc/passwd\n",
			target: ErrInvalidBasePromptPath,
		},
		{
			name:   "escape-base-prompt",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: ../etc/passwd\n",
			target: ErrInvalidBasePromptPath,
		},
		{
			name:   "dot-base-prompt",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: ./\n",
			target: ErrInvalidBasePromptPath,
		},
		{
			name:   "bad-tool-slug",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: x.md\ndeclared_tools:\n  - Issue-Read\n",
			target: ErrInvalidDeclaredTool,
		},
		{
			name:   "empty-tool-slug",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: x.md\ndeclared_tools:\n  - \"\"\n",
			target: ErrInvalidDeclaredTool,
		},
		{
			name:   "host-field-container",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: x.md\ncontainer: { image: foo }\n",
			target: ErrAgentSchemaForbiddenField,
		},
		{
			name:   "host-field-roles",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: x.md\nroles: {}\n",
			target: ErrAgentSchemaForbiddenField,
		},
		{
			name:   "host-field-llm",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: x.md\nllm:\n  model: foo\n",
			target: ErrAgentSchemaForbiddenField,
		},
		{
			name:   "unknown-top-level",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: x.md\nweird: 1\n",
			target: ErrUnknownField,
		},
		{
			name:   "unknown-entry-field",
			body:   "version: 1\nkind: agent\nentry:\n  base_prompt: x.md\n  extra: 1\n",
			target: ErrUnknownField,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgentManifest([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected err, got nil")
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("got %v, want errors.Is %v", err, tc.target)
			}
		})
	}
}

func TestParseAgentManifest_EmptyBody(t *testing.T) {
	t.Parallel()

	_, err := ParseAgentManifest([]byte(""))
	if err == nil {
		t.Fatalf("expected err")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty-body err, got %v", err)
	}
}
