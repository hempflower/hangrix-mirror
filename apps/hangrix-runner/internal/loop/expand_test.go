package loop

import (
	"testing"
)

func TestExpandEnv(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		repoVars map[string]string
		want     map[string]string
		wantErr  bool
		errSub   string // substring to look for in error
	}{
		{
			name:     "empty repo vars — no-op",
			env:      map[string]string{"FOO": "${BAR}"},
			repoVars: nil,
			want:     map[string]string{"FOO": "${BAR}"},
		},
		{
			name: "whole-value expansion",
			env: map[string]string{
				"OPENAI_API_KEY": "${OPENAI_API_KEY}",
				"NODE_ENV":       "development",
			},
			repoVars: map[string]string{"OPENAI_API_KEY": "sk-abc123"},
			want: map[string]string{
				"OPENAI_API_KEY": "sk-abc123",
				"NODE_ENV":       "development",
			},
		},
		{
			name: "multiple expansions",
			env: map[string]string{
				"A": "${X}",
				"B": "${Y}",
				"C": "literal",
			},
			repoVars: map[string]string{"X": "val_x", "Y": "val_y"},
			want: map[string]string{
				"A": "val_x",
				"B": "val_y",
				"C": "literal",
			},
		},
		{
			name: "missing variable — error",
			env: map[string]string{
				"KEY": "${MISSING}",
			},
			repoVars: map[string]string{"OTHER": "val"},
			wantErr:  true,
			errSub:   `"MISSING"`,
		},
		{
			name: "missing variable names the env key",
			env: map[string]string{
				"MY_SECRET": "${UNDEFINED_VAR}",
			},
			repoVars: map[string]string{},
			wantErr:  true,
			errSub:   `"MY_SECRET"`,
		},
		{
			name: "partial reference passes through unchanged",
			env: map[string]string{
				"FOO": "prefix-${BAR}",
			},
			repoVars: map[string]string{"BAR": "baz"},
			want: map[string]string{
				"FOO": "prefix-${BAR}",
			},
		},
		{
			name: "two refs concatenated passes through",
			env: map[string]string{
				"FOO": "${A}-${B}",
			},
			repoVars: map[string]string{"A": "1", "B": "2"},
			want: map[string]string{
				"FOO": "${A}-${B}",
			},
		},
		{
			name: "shell default syntax passes through",
			env: map[string]string{
				"FOO": "${VAR:-default}",
			},
			repoVars: map[string]string{"VAR": "val"},
			want: map[string]string{
				"FOO": "${VAR:-default}",
			},
		},
		{
			name: "empty braces passes through",
			env: map[string]string{
				"FOO": "${}",
			},
			repoVars: map[string]string{},
			want: map[string]string{
				"FOO": "${}",
			},
		},
		{
			name: "invalid var name starting with digit passes through",
			env: map[string]string{
				"FOO": "${1BAD}",
			},
			repoVars: map[string]string{"1BAD": "val"},
			want: map[string]string{
				"FOO": "${1BAD}",
			},
		},
		{
			name:     "too short value passes through",
			env:      map[string]string{"X": "${}"},
			repoVars: map[string]string{},
			want:     map[string]string{"X": "${}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clone the input because expandEnv mutates in place.
			env := make(map[string]string, len(tt.env))
			for k, v := range tt.env {
				env[k] = v
			}
			err := expandEnv(env, tt.repoVars)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errSub != "" {
					if !contains(err.Error(), tt.errSub) {
						t.Errorf("error %q does not contain %q", err.Error(), tt.errSub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, wantV := range tt.want {
				gotV, ok := env[k]
				if !ok {
					t.Errorf("key %q missing from expanded env", k)
					continue
				}
				if gotV != wantV {
					t.Errorf("env[%q] = %q, want %q", k, gotV, wantV)
				}
			}
			if len(env) != len(tt.want) {
				t.Errorf("expanded env has %d keys, want %d", len(env), len(tt.want))
			}
		})
	}
}

func TestIsEnvVarName(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"FOO", true},
		{"foo", true},
		{"_bar", true},
		{"A_B", true},
		{"ABC123", true},
		{"a1b2c3", true},
		{"", false},
		{"1BAD", false},
		{"has-dash", false},
		{"has.dot", false},
		{"has space", false},
	}
	for _, tt := range tests {
		got := isEnvVarName(tt.s)
		if got != tt.want {
			t.Errorf("isEnvVarName(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchSub(s, sub)
}

func searchSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
