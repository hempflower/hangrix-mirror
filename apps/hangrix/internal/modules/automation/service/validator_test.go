package service

import (
	"strings"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
)

func TestValidateTask(t *testing.T) {
	v := NewValidator()

	cases := []struct {
		name    string
		task    *agentsconfig.Task
		wantErr bool
		substr  string
	}{
		{
			name:    "valid 5-field cron",
			task:    &agentsconfig.Task{Name: "t1", Schedule: "0 8 * * 1"},
			wantErr: false,
		},
		{
			name:    "valid every minute",
			task:    &agentsconfig.Task{Name: "t2", Schedule: "* * * * *"},
			wantErr: false,
		},
		{
			name:    "empty schedule",
			task:    &agentsconfig.Task{Name: "t3", Schedule: ""},
			wantErr: true,
			substr:  "schedule: required",
		},
		{
			name:    "invalid cron expression",
			task:    &agentsconfig.Task{Name: "t4", Schedule: "not a cron"},
			wantErr: true,
			substr:  "schedule",
		},
		{
			name:    "too few fields",
			task:    &agentsconfig.Task{Name: "t5", Schedule: "* * *"},
			wantErr: true,
			substr:  "schedule",
		},
		{
			name:    "@daily not supported (v1 only 5-field)",
			task:    &agentsconfig.Task{Name: "t6", Schedule: "@daily"},
			wantErr: true,
			substr:  "schedule",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.ValidateTask(tc.task)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.substr != "" && err != nil {
				if !strings.Contains(err.Error(), tc.substr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.substr)
				}
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	v := NewValidator()

	t.Run("all valid", func(t *testing.T) {
		cfg := &agentsconfig.AutomationConfig{
			Tasks: []*agentsconfig.Task{
				{Name: "t1", Schedule: "0 0 * * *", Issue: agentsconfig.IssueSpec{Title: "x"}, Roles: []string{"r"}},
				{Name: "t2", Schedule: "30 12 * * 1-5", Issue: agentsconfig.IssueSpec{Title: "y"}, Roles: []string{"a", "b"}},
			},
		}
		if err := v.ValidateConfig(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("one bad schedule", func(t *testing.T) {
		cfg := &agentsconfig.AutomationConfig{
			Tasks: []*agentsconfig.Task{
				{Name: "good", Schedule: "0 0 * * *", Issue: agentsconfig.IssueSpec{Title: "x"}, Roles: []string{"r"}},
				{Name: "bad", Schedule: "invalid", Issue: agentsconfig.IssueSpec{Title: "y"}, Roles: []string{"r"}},
			},
		}
		err := v.ValidateConfig(cfg)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "tasks[1]") {
			t.Errorf("error should reference task index: %v", err)
		}
		if !strings.Contains(err.Error(), "bad") {
			t.Errorf("error should reference task name: %v", err)
		}
	})

	t.Run("nil task", func(t *testing.T) {
		cfg := &agentsconfig.AutomationConfig{
			Tasks: []*agentsconfig.Task{nil},
		}
		err := v.ValidateConfig(cfg)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "tasks[0]: is nil") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty schedule skipped by validator", func(t *testing.T) {
		// Validator.ValidateTask catches empty schedule (not a cron parse error).
		cfg := &agentsconfig.AutomationConfig{
			Tasks: []*agentsconfig.Task{
				{Name: "t1", Schedule: "", Issue: agentsconfig.IssueSpec{Title: "x"}, Roles: []string{"r"}},
			},
		}
		err := v.ValidateConfig(cfg)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "schedule: required") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestParse(t *testing.T) {
	v := NewValidator()

	sched, err := v.Parse("0 8 * * 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sched == nil {
		t.Fatal("expected non-nil schedule")
	}

	_, err = v.Parse("invalid")
	if err == nil {
		t.Fatal("expected error for invalid expression")
	}
}

func TestNext(t *testing.T) {
	v := NewValidator()

	next, err := v.Next("* * * * *")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if next.Before(time.Now().Add(-time.Second)) {
		t.Errorf("Next(%q) = %v, should be in the future", "* * * * *", next)
	}
	if next.After(time.Now().Add(2 * time.Minute)) {
		t.Errorf("Next(%q) = %v, should be within a minute", "* * * * *", next)
	}

	_, err = v.Next("invalid")
	if err == nil {
		t.Fatal("expected error for invalid expression")
	}
}
