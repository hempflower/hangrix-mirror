package agentsconfig

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

// mapFileProvider is a map-backed FileProvider for testing LoadHostConfig
// without touching git or the filesystem. Keys are repo-relative paths.
// ListDir returns the direct children of dir (paths that have dir as a
// prefix and no further `/` after it).
type mapFileProvider struct {
	files map[string][]byte
}

func (p *mapFileProvider) ReadFile(path string) ([]byte, bool) {
	b, ok := p.files[path]
	return b, ok
}

func (p *mapFileProvider) ListDir(dir string) ([]string, bool) {
	prefix := dir
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	var out []string
	for path := range p.files {
		if len(path) <= len(prefix) || path[:len(prefix)] != prefix {
			continue
		}
		rest := path[len(prefix):]
		// Direct children only: no further path separator.
		if indexOfSlash(rest) >= 0 {
			continue
		}
		out = append(out, path)
	}
	if len(out) == 0 {
		return nil, false
	}
	sort.Strings(out)
	return out, true
}

func indexOfSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// TestParseAgentFile_Happy pins the contract: front matter (triggers,
// permission, tools rule-refs, scope, mcp, llm) is parsed; the Markdown
// body after the closing fence becomes the trimmed Role.Prompt. Tool
// patterns stay nil (resolved later at assembly).
func TestParseAgentFile_Happy(t *testing.T) {
	t.Parallel()

	md := `---
triggers:
  issue.comment:
    mentioned_only: true
    from_users: [alice, bob]
permission: write
tools: [worker]
scope: { paths: ["apps/api/**", "internal/**"] }
mcp: [playwright]
llm:
  model: claude-opus-4-7
  reasoning_effort: high
---
You are the backend worker.

Always git pull --rebase before push.
`
	role, err := ParseAgentFile("backend", []byte(md))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if role.Permission != "write" {
		t.Fatalf("permission = %q, want write", role.Permission)
	}
	if want := []string{"worker"}; !reflect.DeepEqual(role.Tools, want) {
		t.Fatalf("tools = %v, want %v", role.Tools, want)
	}
	if role.ToolPatterns != nil {
		t.Fatalf("ToolPatterns should be nil before assembly, got %v", role.ToolPatterns)
	}
	cmt := role.Triggers[TriggerIssueComment]
	if cmt == nil || cmt.Comment == nil || !cmt.Comment.MentionedOnly {
		t.Fatalf("issue.comment mentioned_only: %+v", cmt)
	}
	if want := []string{"alice", "bob"}; !reflect.DeepEqual(cmt.Comment.FromUsers, want) {
		t.Fatalf("from_users = %v, want %v", cmt.Comment.FromUsers, want)
	}
	if want := []string{"apps/api/**", "internal/**"}; !reflect.DeepEqual(role.Scope.Paths, want) {
		t.Fatalf("scope.paths = %v, want %v", role.Scope.Paths, want)
	}
	if want := []string{"playwright"}; !reflect.DeepEqual(role.MCP, want) {
		t.Fatalf("mcp = %v, want %v", role.MCP, want)
	}
	if role.LLM == nil || role.LLM.Model != "claude-opus-4-7" {
		t.Fatalf("llm = %+v", role.LLM)
	}
	if role.LLM.ReasoningEffort == nil || *role.LLM.ReasoningEffort != "high" {
		t.Fatalf("llm.reasoning_effort = %v", role.LLM.ReasoningEffort)
	}
	// The body after the closing fence, trimmed, is the prompt.
	wantPrompt := "You are the backend worker.\n\nAlways git pull --rebase before push."
	if role.Prompt != wantPrompt {
		t.Fatalf("prompt = %q, want %q", role.Prompt, wantPrompt)
	}
}

// TestParseAgentFile_PermissionDefaultsToRead: an omitted permission
// front-matter field defaults to "read" (fail-safe).
func TestParseAgentFile_PermissionDefaultsToRead(t *testing.T) {
	t.Parallel()

	md := `---
triggers:
  issue.opened: {}
---
hi
`
	role, err := ParseAgentFile("dispatcher", []byte(md))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if role.Permission != "read" {
		t.Fatalf("permission = %q, want read (default)", role.Permission)
	}
}

func TestParseAgentFile_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		roleKey string
		body    string
		target  error
	}{
		{
			name:    "missing-front-matter-fence",
			roleKey: "r",
			body:    "no front matter here, just a body\n",
			target:  ErrInvalidAgentFile,
		},
		{
			name:    "unterminated-front-matter",
			roleKey: "r",
			body:    "---\ntriggers:\n  issue.opened: {}\nstill in front matter\n",
			target:  ErrInvalidAgentFile,
		},
		{
			name:    "empty-body",
			roleKey: "r",
			body:    "---\ntriggers:\n  issue.opened: {}\n---\n   \n",
			target:  ErrInvalidAgentFile,
		},
		{
			name:    "bad-permission",
			roleKey: "r",
			body:    "---\ntriggers:\n  issue.opened: {}\npermission: admin\n---\nhi\n",
			target:  ErrInvalidPermission,
		},
		{
			name:    "empty-triggers",
			roleKey: "r",
			body:    "---\ntriggers: {}\n---\nhi\n",
			target:  ErrEmptyTriggers,
		},
		{
			name:    "missing-triggers",
			roleKey: "r",
			body:    "---\npermission: read\n---\nhi\n",
			target:  ErrEmptyTriggers,
		},
		{
			name:    "unknown-trigger",
			roleKey: "r",
			body:    "---\ntriggers:\n  issue.weird: {}\n---\nhi\n",
			target:  ErrUnknownTrigger,
		},
		{
			name:    "invalid-role-key",
			roleKey: "Bad_Key",
			body:    "---\ntriggers:\n  issue.opened: {}\n---\nhi\n",
			target:  ErrInvalidAgentFile,
		},
		{
			name:    "empty-mcp-name",
			roleKey: "r",
			body:    "---\ntriggers:\n  issue.opened: {}\nmcp: [\"\", playwright]\n---\nhi\n",
			target:  ErrInvalidMCP,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgentFile(tc.roleKey, []byte(tc.body))
			if err == nil {
				t.Fatalf("expected err, got nil")
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("got %v, want errors.Is %v", err, tc.target)
			}
		})
	}
}

// agentsYAMLWithTools is a team-only agents.yml with two tool rules used
// by the LoadHostConfig tests below.
const agentsYAMLWithTools = `version: 1
container:
  image: ghcr.io/acme/dev:1
llm:
  model: claude-sonnet-4-6
tools:
  read-only: [issue_read, issue_comment]
  voter: [issue_read, issue_review_vote]
`

func TestLoadHostConfig_Happy(t *testing.T) {
	t.Parallel()

	fp := &mapFileProvider{files: map[string][]byte{
		HostConfigPath: []byte(agentsYAMLWithTools),
		AgentsDir + "/worker.md": []byte(`---
triggers:
  issue.comment: { mentioned_only: true }
permission: read
tools: [read-only]
---
worker prompt
`),
		AgentsDir + "/reviewer.md": []byte(`---
triggers:
  commit.pushed: {}
permission: write
tools: [read-only, voter]
---
reviewer prompt
`),
	}}

	cfg, err := LoadHostConfig(fp)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg == nil {
		t.Fatalf("LoadHostConfig returned nil")
	}
	if len(cfg.Roles) != 2 {
		t.Fatalf("roles = %d, want 2", len(cfg.Roles))
	}

	worker := cfg.Roles["worker"]
	if worker == nil {
		t.Fatalf("worker role missing")
	}
	if worker.Prompt != "worker prompt" {
		t.Fatalf("worker prompt = %q", worker.Prompt)
	}
	// read-only rule's globs resolve verbatim.
	if want := []string{"issue_read", "issue_comment"}; !reflect.DeepEqual(worker.ToolPatterns, want) {
		t.Fatalf("worker ToolPatterns = %v, want %v", worker.ToolPatterns, want)
	}

	reviewer := cfg.Roles["reviewer"]
	if reviewer == nil {
		t.Fatalf("reviewer role missing")
	}
	// Union of read-only + voter, deduped (issue_read appears in both).
	if want := []string{"issue_read", "issue_comment", "issue_review_vote"}; !reflect.DeepEqual(reviewer.ToolPatterns, want) {
		t.Fatalf("reviewer ToolPatterns = %v, want %v (deduped union)", reviewer.ToolPatterns, want)
	}
}

// TestLoadHostConfig_UndefinedRule: a role referencing a tool rule not
// declared in agents.yml fails with ErrInvalidToolRule.
func TestLoadHostConfig_UndefinedRule(t *testing.T) {
	t.Parallel()

	fp := &mapFileProvider{files: map[string][]byte{
		HostConfigPath: []byte(agentsYAMLWithTools),
		AgentsDir + "/worker.md": []byte(`---
triggers:
  issue.opened: {}
tools: [does-not-exist]
---
hi
`),
	}}

	_, err := LoadHostConfig(fp)
	if !errors.Is(err, ErrInvalidToolRule) {
		t.Fatalf("err = %v, want ErrInvalidToolRule", err)
	}
}

// TestLoadHostConfig_MissingAgentsYAML: a repo with no agents.yml is a
// valid non-agent state — (nil, nil).
func TestLoadHostConfig_MissingAgentsYAML(t *testing.T) {
	t.Parallel()

	fp := &mapFileProvider{files: map[string][]byte{}}
	cfg, err := LoadHostConfig(fp)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for missing agents.yml, got %+v", cfg)
	}
}

// TestLoadHostConfig_NoAgentFiles: agents.yml present but no role files
// under .hangrix/agents → ErrEmptyRoles (a team with no roles is a
// misconfiguration, not a valid degenerate case).
func TestLoadHostConfig_NoAgentFiles(t *testing.T) {
	t.Parallel()

	fp := &mapFileProvider{files: map[string][]byte{
		HostConfigPath: []byte(agentsYAMLWithTools),
	}}
	_, err := LoadHostConfig(fp)
	if !errors.Is(err, ErrEmptyRoles) {
		t.Fatalf("err = %v, want ErrEmptyRoles", err)
	}
}
