package agentsconfig

import (
	"reflect"
	"testing"
)

func TestParseMentions(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single mention",
			in:   "hey @agent-backend please add /healthz",
			want: []string{"backend"},
		},
		{
			name: "multiple mentions deduped, document order",
			in:   "@agent-dispatcher cc @agent-backend cc @agent-dispatcher",
			want: []string{"dispatcher", "backend"},
		},
		{
			name: "skip fenced code block",
			in:   "see below:\n```\n@agent-backend should not match\n```\nbut @agent-reviewer should",
			want: []string{"reviewer"},
		},
		{
			name: "tilde fence also skipped",
			in:   "~~~\n@agent-backend nope\n~~~\n@agent-reviewer yes",
			want: []string{"reviewer"},
		},
		{
			name: "fenced block markers must agree (~~~ does not close ```)",
			in:   "```\n~~~not closing\n@agent-backend still inside\n```\n@agent-reviewer ok",
			want: []string{"reviewer"},
		},
		{
			name: "skip indented code block",
			in:   "regular\n    @agent-backend indented\n\tand @agent-frontend tabbed",
			want: nil,
		},
		{
			name: "skip quote block",
			in:   "> @agent-backend quoted\n@agent-reviewer alive",
			want: []string{"reviewer"},
		},
		{
			name: "skip inline code span",
			in:   "use `@agent-backend` literally; ping @agent-reviewer for real",
			want: []string{"reviewer"},
		},
		{
			name: "multi-tick inline code",
			in:   "see ``@agent-backend with `inside``: still skipped; @agent-reviewer wakes",
			want: []string{"reviewer"},
		},
		{
			name: "unbalanced backtick keeps text literal",
			in:   "stray `tick @agent-reviewer keeps going",
			want: []string{"reviewer"},
		},
		{
			name: "boundary: email-like prefix should not match",
			in:   "ops@agent-backend looks fake; @agent-backend real",
			want: []string{"backend"},
		},
		{
			name: "boundary: trailing dot stops parse cleanly",
			in:   "@agent-backend. done.",
			want: []string{"backend"},
		},
		{
			name: "uppercase role-key rejected by grammar",
			in:   "@agent-Backend",
			want: nil,
		},
		{
			name: "empty body",
			in:   "",
			want: nil,
		},
		{
			name: "hyphen in role key",
			in:   "@agent-backend-coder ping",
			want: []string{"backend-coder"},
		},
		{
			name: "fence with info string still counts",
			in:   "```bash\n@agent-backend skipped\n```\n@agent-reviewer yes",
			want: []string{"reviewer"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseMentions(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseMentions(%q): got %#v want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestHasBacktickWrappedMention(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "single-backtick wrapped mention",
			in:   "use `@agent-server` to ping",
			want: true,
		},
		{
			name: "dual-backtick wrapped mention",
			in:   "see ``@agent-backend`` for details",
			want: true,
		},
		{
			name: "bare mention not wrapped",
			in:   "hey @agent-server please help",
			want: false,
		},
		{
			name: "mention inside fenced code block — skipped",
			in:   "```\n`@agent-server`\n```\n@agent-reviewer ok",
			want: false,
		},
		{
			name: "mention inside indented code — skipped",
			in:   "    `@agent-server`",
			want: false,
		},
		{
			name: "mention inside quote block — skipped",
			in:   "> `@agent-server`",
			want: false,
		},
		{
			name: "no mention at all",
			in:   "just some text with `code` spans",
			want: false,
		},
		{
			name: "empty body",
			in:   "",
			want: false,
		},
		{
			name: "backtick wrapped mention mid-sentence",
			in:   "please route to `@agent-backend` for the fix",
			want: true,
		},
		{
			name: "multiple spans, one has mention",
			in:   "`code` and `@agent-reviewer` in same line",
			want: true,
		},
		{
			name: "unbalanced backtick — mention is literal",
			in:   "stray `@agent-server oops",
			want: false,
		},
		{
			name: "@agent- without backtick is fine",
			in:   "@agent-backend please review",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HasBacktickWrappedMention(tc.in)
			if got != tc.want {
				t.Fatalf("HasBacktickWrappedMention(%q): got %v want %v", tc.in, got, tc.want)
			}
		})
	}
}
