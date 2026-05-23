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
