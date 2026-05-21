package domain

import (
	"encoding/json"
	"testing"
	"time"
)

// helpers ------------------------------------------------------------

func mustPayload(t *testing.T, v ReviewVoteValue, headSHA string) []byte {
	t.Helper()
	b, err := json.Marshal(ReviewVotePayload{Value: v, HeadSHA: headSHA})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func mustPayloadWithReason(t *testing.T, v ReviewVoteValue, headSHA, reason string) []byte {
	t.Helper()
	b, err := json.Marshal(ReviewVotePayload{Value: v, HeadSHA: headSHA, Reason: reason})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func agentEvent(role string, at time.Time, payload []byte) *Event {
	return &Event{
		Kind:      EventReviewVote,
		AgentRole: role,
		CreatedAt: at,
		Payload:   payload,
	}
}

func userEvent(id int64, name string, at time.Time, payload []byte) *Event {
	return &Event{
		Kind:      EventReviewVote,
		ActorID:   id,
		ActorName: name,
		CreatedAt: at,
		Payload:   payload,
	}
}

// tests --------------------------------------------------------------

func TestComputeReviewStatus_EmptyHeadSHA(t *testing.T) {
	issue := &Issue{HeadSHA: ""}
	rs := ComputeReviewStatus(issue, nil)

	if rs.Verdict != ReviewVerdictPending {
		t.Errorf("verdict = %s, want pending", rs.Verdict)
	}
	if !rs.MergeBlocked {
		t.Error("MergeBlocked should be true")
	}
	if rs.BlockReason != "issue branch has no commits yet" {
		t.Errorf("BlockReason = %q, want %q", rs.BlockReason, "issue branch has no commits yet")
	}
	if len(rs.Votes) != 0 {
		t.Errorf("got %d effective votes, want 0", len(rs.Votes))
	}
}

func TestComputeReviewStatus_NoVotes(t *testing.T) {
	issue := &Issue{HeadSHA: "abc123"}
	rs := ComputeReviewStatus(issue, nil)

	if rs.Verdict != ReviewVerdictPending {
		t.Errorf("verdict = %s, want pending", rs.Verdict)
	}
	if !rs.MergeBlocked {
		t.Error("MergeBlocked should be true")
	}
	if rs.BlockReason != "no review votes yet" {
		t.Errorf("BlockReason = %q, want %q", rs.BlockReason, "no review votes yet")
	}
}

func TestComputeReviewStatus_AllApprove(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		agentEvent("tester", now.Add(-2*time.Minute), mustPayload(t, ReviewVoteApprove, sha)),
		agentEvent("server", now.Add(-1*time.Minute), mustPayload(t, ReviewVoteApprove, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictApproved {
		t.Errorf("verdict = %s, want approved", rs.Verdict)
	}
	if rs.MergeBlocked {
		t.Error("MergeBlocked should be false")
	}
	if rs.BlockReason != "" {
		t.Errorf("BlockReason = %q, want empty", rs.BlockReason)
	}
	if len(rs.Votes) != 2 {
		t.Fatalf("got %d effective votes, want 2", len(rs.Votes))
	}
	// Sorted alphabetically by reviewer key.
	if rs.Votes[0].Reviewer != "server" {
		t.Errorf("votes[0].Reviewer = %q, want server", rs.Votes[0].Reviewer)
	}
	if rs.Votes[1].Reviewer != "tester" {
		t.Errorf("votes[1].Reviewer = %q, want tester", rs.Votes[1].Reviewer)
	}
}

func TestComputeReviewStatus_ChangesRequested(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		agentEvent("tester", now.Add(-2*time.Minute), mustPayload(t, ReviewVoteApprove, sha)),
		agentEvent("server", now.Add(-1*time.Minute), mustPayload(t, ReviewVoteRequestChanges, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictChangesRequested {
		t.Errorf("verdict = %s, want changes_requested", rs.Verdict)
	}
	if !rs.MergeBlocked {
		t.Error("MergeBlocked should be true")
	}
	if rs.BlockReason != "changes requested by reviewer" {
		t.Errorf("BlockReason = %q, want %q", rs.BlockReason, "changes requested by reviewer")
	}
	if len(rs.Votes) != 2 {
		t.Fatalf("got %d effective votes, want 2", len(rs.Votes))
	}
}

func TestComputeReviewStatus_AllAbstain(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		agentEvent("tester", now, mustPayload(t, ReviewVoteAbstain, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictPending {
		t.Errorf("verdict = %s, want pending", rs.Verdict)
	}
	if !rs.MergeBlocked {
		t.Error("MergeBlocked should be true")
	}
	if rs.BlockReason != "waiting for review" {
		t.Errorf("BlockReason = %q, want %q", rs.BlockReason, "waiting for review")
	}
	// Abstain votes are NOT included in effective votes
	if len(rs.Votes) != 0 {
		t.Errorf("got %d effective votes, want 0 (abstain excluded)", len(rs.Votes))
	}
}

func TestComputeReviewStatus_StaleVotesOnly(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	oldSHA := "old456"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		agentEvent("tester", now, mustPayload(t, ReviewVoteApprove, oldSHA)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictPending {
		t.Errorf("verdict = %s, want pending", rs.Verdict)
	}
	if !rs.MergeBlocked {
		t.Error("MergeBlocked should be true")
	}
	if rs.BlockReason != "all review votes are stale — re-review required after latest push" {
		t.Errorf("BlockReason = %q, want %q", rs.BlockReason, "all review votes are stale — re-review required after latest push")
	}
	if len(rs.Votes) != 0 {
		t.Errorf("got %d effective votes, want 0", len(rs.Votes))
	}
	if len(rs.StaleVotes) != 1 {
		t.Fatalf("got %d stale votes, want 1", len(rs.StaleVotes))
	}
	if rs.StaleVotes[0].VoteHeadSHA != oldSHA {
		t.Errorf("StaleVotes[0].VoteHeadSHA = %q, want %q", rs.StaleVotes[0].VoteHeadSHA, oldSHA)
	}
	if rs.StaleVotes[0].Reviewer != "tester" {
		t.Errorf("StaleVotes[0].Reviewer = %q, want tester", rs.StaleVotes[0].Reviewer)
	}
}

func TestComputeReviewStatus_LatestVotePerReviewerWins(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	// Same reviewer (tester) votes twice — only latest should count
	events := []*Event{
		agentEvent("tester", now.Add(-2*time.Minute), mustPayload(t, ReviewVoteRequestChanges, sha)),
		agentEvent("tester", now, mustPayload(t, ReviewVoteApprove, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictApproved {
		t.Errorf("verdict = %s, want approved (latest vote wins)", rs.Verdict)
	}
	if rs.MergeBlocked {
		t.Error("MergeBlocked should be false")
	}
	if len(rs.Votes) != 1 {
		t.Fatalf("got %d effective votes, want 1", len(rs.Votes))
	}
	if rs.Votes[0].Value != ReviewVoteApprove {
		t.Errorf("effective vote = %s, want approve", rs.Votes[0].Value)
	}
}

func TestComputeReviewStatus_StaleBecomesEffectiveAfterReVote(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	oldSHA := "old456"
	issue := &Issue{HeadSHA: sha}

	// Tester first voted on oldSHA (stale), then re-voted on current sha
	events := []*Event{
		agentEvent("tester", now.Add(-2*time.Minute), mustPayload(t, ReviewVoteApprove, oldSHA)),
		agentEvent("tester", now, mustPayload(t, ReviewVoteApprove, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictApproved {
		t.Errorf("verdict = %s, want approved", rs.Verdict)
	}
	if len(rs.Votes) != 1 {
		t.Fatalf("got %d effective votes, want 1", len(rs.Votes))
	}
	if len(rs.StaleVotes) != 0 {
		t.Errorf("got %d stale votes, want 0 (latest is effective)", len(rs.StaleVotes))
	}
}

func TestComputeReviewStatus_MixedEffectiveAndStale(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	oldSHA := "old456"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		agentEvent("tester", now.Add(-2*time.Minute), mustPayload(t, ReviewVoteApprove, sha)),
		agentEvent("server", now, mustPayload(t, ReviewVoteApprove, oldSHA)),
	}
	rs := ComputeReviewStatus(issue, events)

	// tester's vote is effective (matches sha), server's is stale
	if rs.Verdict != ReviewVerdictApproved {
		t.Errorf("verdict = %s, want approved", rs.Verdict)
	}
	if len(rs.Votes) != 1 {
		t.Fatalf("got %d effective votes, want 1", len(rs.Votes))
	}
	if rs.Votes[0].Reviewer != "tester" {
		t.Errorf("effective reviewer = %q, want tester", rs.Votes[0].Reviewer)
	}
	if len(rs.StaleVotes) != 1 {
		t.Fatalf("got %d stale votes, want 1", len(rs.StaleVotes))
	}
	if rs.StaleVotes[0].Reviewer != "server" {
		t.Errorf("stale reviewer = %q, want server", rs.StaleVotes[0].Reviewer)
	}
}

func TestComputeReviewStatus_HumanReviewer(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		userEvent(42, "alice", now, mustPayload(t, ReviewVoteApprove, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictApproved {
		t.Errorf("verdict = %s, want approved", rs.Verdict)
	}
	if len(rs.Votes) != 1 {
		t.Fatalf("got %d effective votes, want 1", len(rs.Votes))
	}
	if rs.Votes[0].Reviewer != "alice" {
		t.Errorf("Reviewer = %q, want alice", rs.Votes[0].Reviewer)
	}
}

func TestComputeReviewStatus_ReasonPreserved(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		agentEvent("tester", now, mustPayloadWithReason(t, ReviewVoteApprove, sha, "LGTM!")),
	}
	rs := ComputeReviewStatus(issue, events)

	if len(rs.Votes) != 1 {
		t.Fatalf("got %d effective votes, want 1", len(rs.Votes))
	}
	if rs.Votes[0].Reason != "LGTM!" {
		t.Errorf("Reason = %q, want LGTM!", rs.Votes[0].Reason)
	}
}

func TestComputeReviewStatus_IgnoresNonReviewVoteEvents(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		{Kind: EventCommitPushed, CreatedAt: now},
		{Kind: EventStateChanged, CreatedAt: now},
		agentEvent("tester", now, mustPayload(t, ReviewVoteApprove, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictApproved {
		t.Errorf("verdict = %s, want approved", rs.Verdict)
	}
}

func TestComputeReviewStatus_InvalidPayloadSkipped(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		{Kind: EventReviewVote, AgentRole: "tester", CreatedAt: now, Payload: []byte("not json")},
		agentEvent("tester", now, mustPayload(t, ReviewVoteApprove, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	// Only the valid payload should count
	if rs.Verdict != ReviewVerdictApproved {
		t.Errorf("verdict = %s, want approved (invalid payload skipped)", rs.Verdict)
	}
}

func TestComputeReviewStatus_EmptyReviewerKeySkipped(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	// Event with no ActorID and no AgentRole — should produce empty key
	events := []*Event{
		{Kind: EventReviewVote, CreatedAt: now, Payload: mustPayload(t, ReviewVoteApprove, sha)},
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictPending {
		t.Errorf("verdict = %s, want pending (empty key skipped)", rs.Verdict)
	}
	if rs.BlockReason != "no review votes yet" {
		t.Errorf("BlockReason = %q, want %q", rs.BlockReason, "no review votes yet")
	}
}

func TestComputeReviewStatus_EmptyPayloadSkipped(t *testing.T) {
	now := time.Now()
	sha := "abc123"
	issue := &Issue{HeadSHA: sha}

	events := []*Event{
		{Kind: EventReviewVote, AgentRole: "tester", CreatedAt: now, Payload: nil},
		agentEvent("tester", now, mustPayload(t, ReviewVoteApprove, sha)),
	}
	rs := ComputeReviewStatus(issue, events)

	if rs.Verdict != ReviewVerdictApproved {
		t.Errorf("verdict = %s, want approved (nil payload skipped)", rs.Verdict)
	}
}
