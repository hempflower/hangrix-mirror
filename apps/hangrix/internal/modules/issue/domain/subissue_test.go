package domain

import (
	"strings"
	"testing"
)

func TestSubIssueBlock_Empty(t *testing.T) {
	reason, blockers := SubIssueBlock(nil)
	if reason != "" || blockers != nil {
		t.Fatalf("SubIssueBlock(nil) = (%q, %v), want (\"\", nil)", reason, blockers)
	}
	reason, blockers = SubIssueBlock([]*OpenDescendant{})
	if reason != "" || blockers != nil {
		t.Fatalf("SubIssueBlock([]) = (%q, %v), want (\"\", nil)", reason, blockers)
	}
}

func TestSubIssueBlock_DirectOnly(t *testing.T) {
	open := []*OpenDescendant{
		{ID: 100, Number: 42, Title: "Fix login", State: StateOpen, Depth: 1},
		{ID: 101, Number: 43, Title: "Add tests", State: StateOpen, Depth: 1},
	}
	reason, blockers := SubIssueBlock(open)
	if reason == "" {
		t.Fatal("expected non-empty block reason")
	}
	if len(blockers) != 2 {
		t.Fatalf("blockers = %d, want 2", len(blockers))
	}
	if !strings.Contains(reason, "#42") || !strings.Contains(reason, "#43") {
		t.Errorf("reason %q should mention #42 and #43", reason)
	}
	if !strings.Contains(reason, "2 open sub-issue") {
		t.Errorf("reason %q should mention count 2", reason)
	}
	// No indirect descendants — should NOT mention "indirect"
	if strings.Contains(reason, "indirect") {
		t.Errorf("reason %q should not mention indirect descendants when there are none", reason)
	}
}

func TestSubIssueBlock_DeepDescendants(t *testing.T) {
	open := []*OpenDescendant{
		{ID: 100, Number: 42, Title: "Direct child", State: StateOpen, Depth: 1},
		{ID: 200, Number: 55, Title: "Grandchild", State: StateOpen, Depth: 2},
		{ID: 300, Number: 66, Title: "Great-grandchild", State: StateOpen, Depth: 3},
	}
	reason, blockers := SubIssueBlock(open)
	if reason == "" {
		t.Fatal("expected non-empty block reason")
	}
	if len(blockers) != 3 {
		t.Fatalf("blockers = %d, want 3", len(blockers))
	}
	// Should mention "indirect descendant" for the depth>1 entries
	if !strings.Contains(reason, "indirect") {
		t.Errorf("reason %q should mention indirect descendants when depth>1", reason)
	}
	if !strings.Contains(reason, "2 indirect descendant") {
		t.Errorf("reason %q should mention count of indirect descendants", reason)
	}
}

func TestSubIssueBlock_Single(t *testing.T) {
	open := []*OpenDescendant{
		{ID: 100, Number: 7, Title: "One child", State: StateOpen, Depth: 1},
	}
	reason, blockers := SubIssueBlock(open)
	if reason == "" {
		t.Fatal("expected non-empty block reason")
	}
	if len(blockers) != 1 {
		t.Fatalf("blockers = %d, want 1", len(blockers))
	}
	if !strings.Contains(reason, "#7") {
		t.Errorf("reason %q should mention #7", reason)
	}
	if !strings.Contains(reason, "1 open sub-issue") {
		t.Errorf("reason %q should mention count 1", reason)
	}
	// Single direct child, no indirect
	if strings.Contains(reason, "indirect") {
		t.Errorf("reason %q should not mention indirect when depth=1 only", reason)
	}
}
