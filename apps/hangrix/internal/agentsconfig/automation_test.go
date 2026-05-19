package agentsconfig

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAutomationConfig(t *testing.T) {
	t.Run("valid minimal config", func(t *testing.T) {
		raw := []byte(`
version: 1
tasks:
  - name: my-task
    schedule: "0 8 * * 1"
    issue:
      title: "test issue"
    roles:
      - implementer
`)
		cfg, err := ParseAutomationConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Version != 1 {
			t.Fatalf("version: got %d want 1", cfg.Version)
		}
		if len(cfg.Tasks) != 1 {
			t.Fatalf("tasks len: got %d want 1", len(cfg.Tasks))
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		_, err := ParseAutomationConfig([]byte(": bad"))
		if err == nil {
			t.Fatal("expected error for invalid YAML")
		}
	})

	t.Run("empty tasks", func(t *testing.T) {
		raw := []byte("version: 1\ntasks: []\n")
		cfg, err := ParseAutomationConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Tasks) != 0 {
			t.Fatalf("expected 0 tasks, got %d", len(cfg.Tasks))
		}
	})
}

func TestAutomationConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  *AutomationConfig
		errs []string // substrings each expected in the error
	}{
		{
			name: "valid",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: "t1", Schedule: "0 0 * * *", Issue: IssueSpec{Title: "ok"}, Roles: []string{"r"}},
				},
			},
			errs: nil,
		},
		{
			name: "bad version",
			cfg:  &AutomationConfig{Version: 2},
			errs: []string{"version must be 1"},
		},
		{
			name: "nil task",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks:   []*Task{nil},
			},
			errs: []string{"tasks[0]: is nil"},
		},
		{
			name: "empty name",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: "", Schedule: "0 0 * * *", Issue: IssueSpec{Title: "x"}, Roles: []string{"r"}},
				},
			},
			errs: []string{"tasks[0].name: required"},
		},
		{
			name: "name too long",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: strings.Repeat("a", 101), Schedule: "0 0 * * *", Issue: IssueSpec{Title: "x"}, Roles: []string{"r"}},
				},
			},
			errs: []string{"tasks[0].name: max 100 characters"},
		},
		{
			name: "name bad pattern",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: "BadName", Schedule: "0 0 * * *", Issue: IssueSpec{Title: "x"}, Roles: []string{"r"}},
				},
			},
			errs: []string{"tasks[0].name: must match [a-z][a-z0-9-]*"},
		},
		{
			name: "duplicate name",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: "dup", Schedule: "0 0 * * *", Issue: IssueSpec{Title: "x"}, Roles: []string{"r"}},
					{Name: "dup", Schedule: "1 0 * * *", Issue: IssueSpec{Title: "y"}, Roles: []string{"r"}},
				},
			},
			errs: []string{"tasks[1].name: duplicate"},
		},
		{
			name: "missing schedule",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: "t1", Schedule: "", Issue: IssueSpec{Title: "x"}, Roles: []string{"r"}},
				},
			},
			errs: []string{"tasks[0].schedule: required"},
		},
		{
			name: "missing title",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: "t1", Schedule: "0 0 * * *", Issue: IssueSpec{Title: ""}, Roles: []string{"r"}},
				},
			},
			errs: []string{"tasks[0].issue.title: required"},
		},
		{
			name: "title too long",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: "t1", Schedule: "0 0 * * *", Issue: IssueSpec{Title: strings.Repeat("x", 201)}, Roles: []string{"r"}},
				},
			},
			errs: []string{"tasks[0].issue.title: max 200 characters"},
		},
		{
			name: "no roles",
			cfg: &AutomationConfig{
				Version: 1,
				Tasks: []*Task{
					{Name: "t1", Schedule: "0 0 * * *", Issue: IssueSpec{Title: "x"}, Roles: nil},
				},
			},
			errs: []string{"tasks[0].roles: at least one role required"},
		},
		{
			name: "multiple errors",
			cfg: &AutomationConfig{
				Version: 0,
				Tasks: []*Task{
					{Name: "", Schedule: "", Issue: IssueSpec{Title: ""}, Roles: nil},
				},
			},
			errs: []string{"version must be 1", "tasks[0].name: required", "tasks[0].schedule: required", "tasks[0].issue.title: required", "tasks[0].roles: at least one role required"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.errs == nil {
				if err != nil {
					t.Fatalf("expected nil error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var vErr *AutomationConfigValidationError
			if !errors.As(err, &vErr) {
				t.Fatalf("expected *AutomationConfigValidationError, got %T", err)
			}
			for _, want := range tc.errs {
				found := false
				for _, e := range vErr.Errors {
					if e == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("missing error %q in %v", want, vErr.Errors)
				}
			}
		})
	}
}

func TestAutomationConfigValidationError(t *testing.T) {
	e := &AutomationConfigValidationError{Errors: []string{"a", "b"}}
	msg := e.Error()
	if !strings.Contains(msg, "automation config validation") {
		t.Errorf("Error() message missing prefix: %s", msg)
	}
	if !strings.Contains(msg, "a") || !strings.Contains(msg, "b") {
		t.Errorf("Error() message missing errors: %s", msg)
	}
}
