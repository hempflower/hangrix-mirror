package domain_test

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agents_config/domain"
)

func TestIsValidTrigger(t *testing.T) {
	t.Parallel()

	valid := []string{
		"issue.opened",
		"issue.closed",
		"issue.comment.any",
		"issue.comment.mentioned",
		"commit.pushed",
		"review_vote.posted",
		"ci.status_changed",
	}
	for _, s := range valid {
		if !domain.IsValidTrigger(s) {
			t.Fatalf("expected %q valid", s)
		}
	}

	invalid := []string{
		"",
		"issue",
		"Issue.Opened",
		"unknown.event",
		"issue.comment", // not the full path
	}
	for _, s := range invalid {
		if domain.IsValidTrigger(s) {
			t.Fatalf("expected %q invalid", s)
		}
	}
}

func TestIsValidMentionBy(t *testing.T) {
	t.Parallel()

	valid := []domain.MentionBy{
		domain.MentionByOwner,
		domain.MentionByCollaborators,
		domain.MentionByAnyone,
	}
	for _, v := range valid {
		if !domain.IsValidMentionBy(v) {
			t.Fatalf("expected %q valid", v)
		}
	}

	invalid := []domain.MentionBy{"", "OWNER", "team", "self"}
	for _, v := range invalid {
		if domain.IsValidMentionBy(v) {
			t.Fatalf("expected %q invalid", v)
		}
	}
}
