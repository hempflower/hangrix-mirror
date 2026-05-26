package agentsconfig

import (
	"errors"
	"reflect"
	"testing"
)

// reviewersHost is a minimal valid host yaml with a reviewers block. The
// `%s` slot lets each test swap in a different reviewers section.
const reviewersHostTmpl = `
version: 1
container:
  image: ghcr.io/acme/dev:1
roles:
  worker:
    triggers: { issue.comment: { mentioned_only: true } }
    permission: read
    prompt: w
  srv-reviewer:
    triggers: { commit.pushed: {} }
    permission: write
    prompt: r
  web-reviewer:
    triggers: { commit.pushed: {} }
    permission: write
    prompt: r
  maintainer:
    triggers: { issue.opened: {} }
    permission: write
    prompt: m
%s`

func parseReviewersHost(t *testing.T, reviewers string) (*HostConfig, error) {
	t.Helper()
	yaml := reviewersHostTmpl[:len(reviewersHostTmpl)-2] + reviewers // drop trailing "%s"
	return ParseHostConfig([]byte(yaml))
}

func TestReviewers_RequiredReviewers(t *testing.T) {
	cfg, err := parseReviewersHost(t, `
reviewers:
  rules:
    - paths: ["apps/api/**", "pkg/**"]
      reviewers: [srv-reviewer]
    - paths: ["apps/web/**"]
      reviewers: [web-reviewer]
  fallback: [maintainer]
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
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
	cfg, err := parseReviewersHost(t, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
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
			_, err := parseReviewersHost(t, tc.reviewers)
			if !errors.Is(err, ErrInvalidReviewers) {
				t.Errorf("err = %v, want ErrInvalidReviewers", err)
			}
		})
	}
}
