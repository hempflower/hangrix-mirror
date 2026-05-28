package envexpand

import (
	"testing"
)

func TestExpandString(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		lookup LookupFunc
		want   string
	}{
		{
			name:   "no variables",
			input:  "plain text",
			lookup: nil,
			want:   "plain text",
		},
		{
			name:   "single variable present",
			input:  "${env:FOO}",
			lookup: fixedLookup("FOO", "bar"),
			want:   "bar",
		},
		{
			name:   "single variable missing",
			input:  "${env:FOO}",
			lookup: missingLookup(),
			want:   "",
		},
		{
			name:   "variable set to empty string",
			input:  "${env:FOO}",
			lookup: fixedLookup("FOO", ""),
			want:   "",
		},
		{
			name:   "two variables present",
			input:  "${env:A}:${env:B}",
			lookup: multiLookup("A", "x", "B", "y"),
			want:   "x:y",
		},
		{
			name:   "variable with surrounding text",
			input:  "${env:DATA_ROOT}/repos",
			lookup: fixedLookup("DATA_ROOT", "/mnt"),
			want:   "/mnt/repos",
		},
		{
			name:   "empty name — malformed, kept as-is",
			input:  "${env:}",
			lookup: nil,
			want:   "${env:}",
		},
		{
			name:   "no colon — malformed, kept as-is",
			input:  "${envFOO}",
			lookup: nil,
			want:   "${envFOO}",
		},
		{
			name:   "name with hyphen — malformed, kept as-is",
			input:  "${env:foo-bar}",
			lookup: nil,
			want:   "${env:foo-bar}",
		},
		{
			name:   "name with underscore",
			input:  "${env:MY_VAR}",
			lookup: fixedLookup("MY_VAR", "ok"),
			want:   "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExpandString(tt.input, tt.lookup)
			if got != tt.want {
				t.Errorf("ExpandString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func fixedLookup(name, value string) LookupFunc {
	return func(n string) (string, bool) {
		if n == name {
			return value, true
		}
		return "", false
	}
}

func missingLookup() LookupFunc {
	return func(string) (string, bool) { return "", false }
}

func multiLookup(pairs ...string) LookupFunc {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs)-1; i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(n string) (string, bool) {
		v, ok := m[n]
		return v, ok
	}
}
