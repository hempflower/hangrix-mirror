package loop

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
)

// The agent builds its contribution-branch ref from HANGRIX_ISSUE_NUMBER
// (issue-<N>/<role>/<slug>); buildAgentEnv must surface task.IssueNumber so
// the branch matches the git ACL namespace the session is allowed to push.
func TestBuildAgentEnvSetsIssueNumber(t *testing.T) {
	env := buildAgentEnv(&client.Task{
		SessionID:     7,
		Role:          "server",
		Model:         "deepseek-v4-pro",
		IssueNumber:   42,
		WorkingBranch: "issue/42",
	}, "https://hangrix.example")

	if got := env["HANGRIX_ISSUE_NUMBER"]; got != "42" {
		t.Fatalf("HANGRIX_ISSUE_NUMBER = %q, want %q", got, "42")
	}
	if got := env["HANGRIX_WORKING_BRANCH"]; got != "issue/42" {
		t.Fatalf("HANGRIX_WORKING_BRANCH = %q, want %q", got, "issue/42")
	}
}

// Non-issue sessions (e.g. admin smoke) carry no issue binding; the env var
// is left unset rather than injected as "0".
func TestBuildAgentEnvOmitsZeroIssueNumber(t *testing.T) {
	env := buildAgentEnv(&client.Task{SessionID: 9, Role: "maintainer"}, "https://hangrix.example")

	if _, ok := env["HANGRIX_ISSUE_NUMBER"]; ok {
		t.Fatalf("HANGRIX_ISSUE_NUMBER set for a non-issue session: %q", env["HANGRIX_ISSUE_NUMBER"])
	}
}

func TestBuildAgentEnvMcpServers(t *testing.T) {
	// Nil McpServers → HANGRIX_MCP_SERVERS is unset.
	env := buildAgentEnv(&client.Task{SessionID: 1, Role: "web"}, "https://hangrix.example")
	if _, ok := env["HANGRIX_MCP_SERVERS"]; ok {
		t.Fatalf("HANGRIX_MCP_SERVERS set when McpServers is nil")
	}

	// Empty McpServers → HANGRIX_MCP_SERVERS is unset.
	env = buildAgentEnv(&client.Task{SessionID: 1, Role: "web", McpServers: []string{}}, "https://hangrix.example")
	if _, ok := env["HANGRIX_MCP_SERVERS"]; ok {
		t.Fatalf("HANGRIX_MCP_SERVERS set when McpServers is empty")
	}

	// Single server → comma-free value.
	env = buildAgentEnv(&client.Task{SessionID: 1, Role: "web", McpServers: []string{"playwright"}}, "https://hangrix.example")
	if got := env["HANGRIX_MCP_SERVERS"]; got != "playwright" {
		t.Fatalf("HANGRIX_MCP_SERVERS = %q, want %q", got, "playwright")
	}

	// Multiple servers → comma-joined.
	env = buildAgentEnv(&client.Task{SessionID: 1, Role: "web", McpServers: []string{"playwright", "github"}}, "https://hangrix.example")
	if got := env["HANGRIX_MCP_SERVERS"]; got != "playwright,github" {
		t.Fatalf("HANGRIX_MCP_SERVERS = %q, want %q", got, "playwright,github")
	}
}
