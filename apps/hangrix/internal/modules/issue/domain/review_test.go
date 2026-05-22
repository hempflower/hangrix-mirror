package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// helpers ------------------------------------------------------------

func contribVote(t *testing.T, v ReviewVoteValue, contribID int64, reviewedSHA, reason string) []byte {
	t.Helper()
	b, err := json.Marshal(ReviewVotePayload{Value: v, ContributionID: contribID, ReviewedSHA: reviewedSHA, Reason: reason})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func agentVoteEvent(role string, at time.Time, payload []byte) *Event {
	return &Event{Kind: EventReviewVote, AgentRole: role, CreatedAt: at, Payload: payload}
}

// ComputeContributionReviewStatus ------------------------------------

func TestContribReview_EmptyHead(t *testing.T) {
	rs := ComputeContributionReviewStatus(&Contribution{ID: 1, HeadSHA: ""}, []string{"server-reviewer"}, nil)
	if rs.Verdict != ReviewVerdictPending || !rs.MergeBlocked {
		t.Fatalf("verdict=%s blocked=%v, want pending+blocked", rs.Verdict, rs.MergeBlocked)
	}
	if rs.BlockReason != "contribution branch has no commits yet" {
		t.Errorf("BlockReason = %q", rs.BlockReason)
	}
}

func TestContribReview_PendingUntilAllRequiredVote(t *testing.T) {
	now := time.Now()
	c := &Contribution{ID: 7, HeadSHA: "head1"}
	// Two required reviewers, only one has voted → still pending.
	events := []*Event{
		agentVoteEvent("server-reviewer", now, contribVote(t, ReviewVoteApprove, 7, "head1", "")),
	}
	rs := ComputeContributionReviewStatus(c, []string{"server-reviewer", "tester"}, events)
	if rs.Verdict != ReviewVerdictPending || !rs.MergeBlocked {
		t.Fatalf("verdict=%s blocked=%v, want pending+blocked", rs.Verdict, rs.MergeBlocked)
	}
	if len(rs.PendingReviewers) != 1 || rs.PendingReviewers[0] != "tester" {
		t.Errorf("PendingReviewers = %v, want [tester]", rs.PendingReviewers)
	}
	if !strings.Contains(rs.BlockReason, "tester") {
		t.Errorf("BlockReason = %q, want mention of tester", rs.BlockReason)
	}
}

func TestContribReview_ApprovedWhenAllRequiredVote(t *testing.T) {
	now := time.Now()
	c := &Contribution{ID: 7, HeadSHA: "head1"}
	events := []*Event{
		agentVoteEvent("server-reviewer", now.Add(-2*time.Minute), contribVote(t, ReviewVoteApprove, 7, "head1", "")),
		agentVoteEvent("tester", now.Add(-time.Minute), contribVote(t, ReviewVoteAbstain, 7, "head1", "")),
	}
	rs := ComputeContributionReviewStatus(c, []string{"server-reviewer", "tester"}, events)
	if rs.Verdict != ReviewVerdictApproved || rs.MergeBlocked {
		t.Fatalf("verdict=%s blocked=%v, want approved+unblocked (all voted approve/abstain)", rs.Verdict, rs.MergeBlocked)
	}
	if len(rs.PendingReviewers) != 0 {
		t.Errorf("PendingReviewers = %v, want empty", rs.PendingReviewers)
	}
}

func TestContribReview_NoRequiredReviewersAutoApproves(t *testing.T) {
	c := &Contribution{ID: 7, HeadSHA: "head1"}
	rs := ComputeContributionReviewStatus(c, nil, nil)
	if rs.Verdict != ReviewVerdictApproved || rs.MergeBlocked {
		t.Fatalf("verdict=%s blocked=%v, want approved (no required reviewers)", rs.Verdict, rs.MergeBlocked)
	}
}

func TestContribReview_AnyRejectIsDominant(t *testing.T) {
	now := time.Now()
	c := &Contribution{ID: 7, HeadSHA: "head1"}
	// A reject lands even before the other required reviewer voted → rejected.
	events := []*Event{
		agentVoteEvent("tester", now, contribVote(t, ReviewVoteReject, 7, "head1", "tests red")),
	}
	rs := ComputeContributionReviewStatus(c, []string{"server-reviewer", "tester"}, events)
	if rs.Verdict != ReviewVerdictRejected || !rs.MergeBlocked {
		t.Fatalf("verdict=%s blocked=%v, want rejected+blocked", rs.Verdict, rs.MergeBlocked)
	}
}

func TestContribReview_RejectFromNonRequiredStillRejects(t *testing.T) {
	now := time.Now()
	c := &Contribution{ID: 7, HeadSHA: "head1"}
	// A reject from a role that is not in the required set still rejects.
	events := []*Event{
		agentVoteEvent("server-reviewer", now.Add(-time.Minute), contribVote(t, ReviewVoteApprove, 7, "head1", "")),
		agentVoteEvent("web-reviewer", now, contribVote(t, ReviewVoteReject, 7, "head1", "concern")),
	}
	rs := ComputeContributionReviewStatus(c, []string{"server-reviewer"}, events)
	if rs.Verdict != ReviewVerdictRejected {
		t.Fatalf("verdict=%s, want rejected (any reject is dominant)", rs.Verdict)
	}
}

func TestContribReview_LatestVoteWins(t *testing.T) {
	now := time.Now()
	c := &Contribution{ID: 7, HeadSHA: "head1"}
	events := []*Event{
		// Reviewer flips reject -> approve; latest (approve) wins.
		agentVoteEvent("server-reviewer", now.Add(-2*time.Minute), contribVote(t, ReviewVoteReject, 7, "head1", "")),
		agentVoteEvent("server-reviewer", now.Add(-time.Minute), contribVote(t, ReviewVoteApprove, 7, "head1", "")),
	}
	rs := ComputeContributionReviewStatus(c, []string{"server-reviewer"}, events)
	if rs.Verdict != ReviewVerdictApproved || rs.MergeBlocked {
		t.Fatalf("verdict=%s blocked=%v, want approved (latest vote wins)", rs.Verdict, rs.MergeBlocked)
	}
}

func TestContribReview_IgnoresOtherContribution(t *testing.T) {
	now := time.Now()
	c := &Contribution{ID: 7, HeadSHA: "head1"}
	events := []*Event{
		agentVoteEvent("server-reviewer", now, contribVote(t, ReviewVoteApprove, 99, "head1", "")), // different contribution
	}
	rs := ComputeContributionReviewStatus(c, []string{"server-reviewer"}, events)
	if rs.Verdict != ReviewVerdictPending {
		t.Fatalf("verdict=%s, want pending (vote belongs to another contribution)", rs.Verdict)
	}
	if len(rs.PendingReviewers) != 1 || rs.PendingReviewers[0] != "server-reviewer" {
		t.Errorf("PendingReviewers = %v, want [server-reviewer]", rs.PendingReviewers)
	}
}

func TestContribReview_StatusMapping(t *testing.T) {
	cases := []struct {
		verdict ReviewVerdict
		want    ContributionStatus
	}{
		{ReviewVerdictApproved, ContribStatusApproved},
		{ReviewVerdictRejected, ContribStatusRejected},
		{ReviewVerdictPending, ContribStatusPending},
	}
	for _, tc := range cases {
		got := (&ReviewStatus{Verdict: tc.verdict}).ContributionStatus()
		if got != tc.want {
			t.Errorf("verdict %s → %s, want %s", tc.verdict, got, tc.want)
		}
	}
}

// IssueMergeBlock -----------------------------------------------------

func TestIssueMergeBlock(t *testing.T) {
	merged := &Contribution{Status: ContribStatusMerged}
	closed := &Contribution{Status: ContribStatusClosed}
	approved := &Contribution{Status: ContribStatusApproved}
	rejected := &Contribution{Status: ContribStatusRejected}
	pending := &Contribution{Status: ContribStatusPending}

	cases := []struct {
		name        string
		contribs    []*Contribution
		branchAhead bool
		wantBlocked bool
		wantSubstr  string
	}{
		{"empty branch, no contribs", nil, false, true, "no changes to merge"},
		{"ahead, no contribs (human push)", nil, true, false, ""},
		{"ahead, all resolved", []*Contribution{merged, closed, rejected}, true, false, ""},
		{"pending blocks", []*Contribution{merged, pending}, true, true, "still pending"},
		{"approved-but-unapplied does not block", []*Contribution{approved}, true, false, ""},
		{"rejected does not block", []*Contribution{rejected}, true, false, ""},
		{"all resolved but branch empty", []*Contribution{merged}, false, true, "no changes to merge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IssueMergeBlock(tc.contribs, tc.branchAhead)
			if (got != "") != tc.wantBlocked {
				t.Fatalf("IssueMergeBlock = %q, wantBlocked=%v", got, tc.wantBlocked)
			}
			if tc.wantSubstr != "" && !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("IssueMergeBlock = %q, want substring %q", got, tc.wantSubstr)
			}
		})
	}
}
