package domain_test

import (
	"errors"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agents_config/domain"
)

func TestParseAgentRef_Happy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want domain.AgentRef
	}{
		{"hangrix/reviewer@v1.0.0", domain.AgentRef{Owner: "hangrix", Name: "reviewer", Ref: "v1.0.0"}},
		{"acme/backend-coder@main", domain.AgentRef{Owner: "acme", Name: "backend-coder", Ref: "main"}},
		{"hangrix/maintainer@deadbeef1234567890abcdef1234567890abcdef12", domain.AgentRef{Owner: "hangrix", Name: "maintainer", Ref: "deadbeef1234567890abcdef1234567890abcdef12"}},
	}

	for _, tc := range cases {
		got, err := domain.ParseAgentRef(tc.in)
		if err != nil {
			t.Fatalf("%q: unexpected err: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %+v want %+v", tc.in, got, tc.want)
		}
		if got.String() != tc.in {
			t.Fatalf("%q: round trip got %q", tc.in, got.String())
		}
	}
}

func TestParseAgentRef_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		in     string
		target error
	}{
		{"empty", "", domain.ErrInvalidAgentRef},
		{"missing-ref-no-at", "hangrix/reviewer", domain.ErrMissingAgentRef},
		{"missing-ref-empty", "hangrix/reviewer@", domain.ErrMissingAgentRef},
		{"missing-slash", "hangrix@v1", domain.ErrInvalidAgentRef},
		{"empty-owner", "/reviewer@v1", domain.ErrInvalidAgentRef},
		{"empty-name", "hangrix/@v1", domain.ErrInvalidAgentRef},
		{"too-many-slashes", "a/b/c@v1", domain.ErrInvalidAgentRef},
		{"multiple-at", "a/b@c@d", domain.ErrInvalidAgentRef},
		{"whitespace", "hangrix/reviewer @v1", domain.ErrInvalidAgentRef},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ParseAgentRef(tc.in)
			if !errors.Is(err, tc.target) {
				t.Fatalf("got err %v, want errors.Is %v", err, tc.target)
			}
		})
	}
}
