package agentsconfig

import (
	"testing"

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
		if !IsValidTrigger(s) {
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
		if IsValidTrigger(s) {
			t.Fatalf("expected %q invalid", s)
		}
	}
}

func TestIsValidMentionBy(t *testing.T) {
	t.Parallel()

	valid := []MentionBy{
		MentionByOwner,
		MentionByCollaborators,
		MentionByAnyone,
	}
	for _, v := range valid {
		if !IsValidMentionBy(v) {
			t.Fatalf("expected %q valid", v)
		}
	}

	invalid := []MentionBy{"", "OWNER", "team", "self"}
	for _, v := range invalid {
		if IsValidMentionBy(v) {
			t.Fatalf("expected %q invalid", v)
		}
	}
}
