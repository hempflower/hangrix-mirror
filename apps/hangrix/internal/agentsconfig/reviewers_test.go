package agentsconfig

import (
	"errors"
	"reflect"
	"testing"
)

// reviewersTeamTmpl is a team-only agents.yml carrying the tool rules the
// reviewer roles reference (a `voter` rule that INCLUDES issue_review_vote
// and a `nonvoter` rule that does NOT) plus the `reviewers:` block. The
// `%s` slot is where each test swaps in a different reviewers section.
const reviewersTeamTmpl = `version: 1
container:
  image: ghcr.io/acme/dev:1
tools:
  voter: [issue_read, issue_comment, issue_review_vote]
  nonvoter: [issue_read, issue_comment]
%s`

// reviewerAgentFiles are the per-role `.hangrix/agents/<role>.md` files
// the reviewers tests share. worker is a permission:write role whose
// tools rule (`nonvoter`) LACKS issue_review_vote so it cannot vote; the
// reviewer roles use the `voter` rule and permission: write so they can.
func reviewerAgentFiles() map[string][]byte {
	return map[string][]byte{
		AgentsDir + "/worker.md": []byte(`---
triggers: { issue.comment: { mentioned_only: true } }
permission: write
tools: [nonvoter]
---
w
`),
		AgentsDir + "/srv-reviewer.md": []byte(`---
triggers: { commit.pushed: {} }
permission: write
tools: [voter]
---
r
`),
		AgentsDir + "/web-reviewer.md": []byte(`---
triggers: { commit.pushed: {} }
permission: write
tools: [voter]
---
r
`),
		AgentsDir + "/maintainer.md": []byte(`---
triggers: { issue.opened: {} }
permission: write
tools: [voter]
---
m
`),
	}
}

// loadReviewersHost builds the host config via the map-backed
// FileProvider: agents.yml (tool rules + reviewers block) plus the shared
// per-role agent files. Reviewer-role existence + vote-capability is
// validated in LoadHostConfig (AssembleHostConfig); structural reviewer
// errors surface from ParseHostConfig — both wrap ErrInvalidReviewers.
func loadReviewersHost(t *testing.T, reviewers string) (*HostConfig, error) {
	t.Helper()
	// reviewersTeamTmpl ends with "%s\n"-less "%s"; substitute directly.
	yaml := reviewersTeamTmpl[:len(reviewersTeamTmpl)-2] + reviewers // drop trailing "%s"
	files := reviewerAgentFiles()
	files[HostConfigPath] = []byte(yaml)
	return LoadHostConfig(&mapFileProvider{files: files})
}

func TestReviewers_RequiredReviewers(t *testing.T) {
	cfg, err := loadReviewersHost(t, `
reviewers:
  rules:
    - paths: ["apps/api/**", "pkg/**"]
      reviewers: [srv-reviewer]
    - paths: ["apps/web/**"]
      reviewers: [web-reviewer]
  fallback: [maintainer]
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Reviewers == nil {
		t.Fatal("expected reviewers config")
	}
	cases := []struct {
		paths []string
		want  []string
	}{
		{[]string{"apps/api/x.go"}, []string{"srv-reviewer"}},
		{[]string{"apps/web/x.vue"}, []string{"web-reviewer"}},
		{[]string{"pkg/util/x.go", "apps/web/y.vue"}, []string{"srv-reviewer", "web-reviewer"}}, // union
		{[]string{"README.md"}, []string{"maintainer"}},                                         // fallback
		{nil, []string{"maintainer"}},                                                           // no paths → fallback
	}
	for _, c := range cases {
		if got := cfg.RequiredReviewers(c.paths); !reflect.DeepEqual(got, c.want) {
			t.Errorf("RequiredReviewers(%v) = %v, want %v", c.paths, got, c.want)
		}
	}
}

func TestReviewers_NilWhenAbsent(t *testing.T) {
	cfg, err := loadReviewersHost(t, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Reviewers != nil {
		t.Errorf("expected nil reviewers when block absent, got %+v", cfg.Reviewers)
	}
	if got := cfg.RequiredReviewers([]string{"anything"}); got != nil {
		t.Errorf("RequiredReviewers with no config = %v, want nil", got)
	}
}

func TestReviewers_Errors(t *testing.T) {
	cases := []struct {
		name      string
		reviewers string
	}{
		{"unknown reviewer role", `
reviewers:
  rules:
    - paths: ["**"]
      reviewers: [nope]
  fallback: [maintainer]
`},
		{"reviewer cannot vote", `
reviewers:
  rules:
    - paths: ["**"]
      reviewers: [worker]
  fallback: [maintainer]
`},
		{"rule missing paths", `
reviewers:
  rules:
    - reviewers: [srv-reviewer]
  fallback: [maintainer]
`},
		{"rule missing reviewers", `
reviewers:
  rules:
    - paths: ["**"]
  fallback: [maintainer]
`},
		{"empty fallback", `
reviewers:
  rules:
    - paths: ["**"]
      reviewers: [srv-reviewer]
`},
		{"fallback cannot vote", `
reviewers:
  fallback: [worker]
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadReviewersHost(t, tc.reviewers)
			if !errors.Is(err, ErrInvalidReviewers) {
				t.Errorf("err = %v, want ErrInvalidReviewers", err)
			}
		})
	}
}
