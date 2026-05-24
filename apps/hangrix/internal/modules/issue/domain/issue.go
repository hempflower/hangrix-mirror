// Package domain declares the issue model — the M4 unit of work that
// combines a conversation, a git branch, and (in M5) an agent session into a
// single entity. Other modules consume the Store interface via the ioc
// container.
package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// State models the lifecycle. Once an issue is merged the branch is gone
// (deleted post-merge), and once closed the branch is preserved but no
// further commits / comments are allowed. Reopening a merged issue is not
// supported — open a fresh one.
type State string

const (
	StateOpen   State = "open"
	StateMerged State = "merged"
	StateClosed State = "closed"
)

func (s State) Valid() bool {
	return s == StateOpen || s == StateMerged || s == StateClosed
}

// EventKind enumerates system-generated entries on the issue timeline. They
// are interleaved with human comments at render time; the union view is
// always sorted by created_at.
type EventKind string

const (
	EventCommitPushed EventKind = "commit_pushed"
	EventBranchMerged EventKind = "branch_merged"
	EventStateChanged EventKind = "state_changed"
	EventTitleChanged EventKind = "title_changed"

	// EventReviewVote records a reviewer (agent or human) voting
	// approve / reject / abstain on a contribution branch. The payload
	// follows ReviewVotePayload. Maintainer roles subscribe to the
	// corresponding spawner trigger (review_vote.posted) so they wake
	// when a vote lands.
	EventReviewVote EventKind = "review_vote"

	// Contribution-branch model (see docs/contribution-branches.md). Each
	// of these carries a ContributionEventPayload.

	// EventContributionPushed fires when an agent pushes (creates or
	// updates) a contribution branch in its per-issue namespace.
	EventContributionPushed EventKind = "contribution_pushed"

	// EventContributionMerged fires when the server merges an approved
	// contribution branch into the issue branch (first-level gate).
	EventContributionMerged EventKind = "contribution_merged"

	// EventContributionRejected fires when a reviewer rejects a
	// contribution branch (votes reject). Because branches are immutable,
	// a rejected branch is revised by pushing a new versioned branch.
	EventContributionRejected EventKind = "contribution_rejected"

	// EventContributionClosed fires when the owning role abandons a
	// contribution branch.
	EventContributionClosed EventKind = "contribution_closed"
)

// ReviewVoteValue enumerates the three vote outcomes a reviewer (agent or
// human) can record. The string values are stable wire format; do not
// rename without a migration.
type ReviewVoteValue string

const (
	ReviewVoteApprove ReviewVoteValue = "approve"
	ReviewVoteReject  ReviewVoteValue = "reject"
	ReviewVoteAbstain ReviewVoteValue = "abstain"
)

// Valid reports whether v is one of the three documented values.
func (v ReviewVoteValue) Valid() bool {
	switch v {
	case ReviewVoteApprove, ReviewVoteReject, ReviewVoteAbstain:
		return true
	}
	return false
}

// ReviewVotePayload is the JSON shape stored in Event.Payload for
// EventReviewVote. Value is the outcome; Reason is the reviewer's free-text
// rationale. Every vote targets a contribution branch: ContributionID names
// it and ReviewedSHA records the contribution head it was cast against.
// Contribution branches are immutable once pushed, so ReviewedSHA always
// equals the contribution head — the field is retained for audit and to
// defend against the rare stale event.
type ReviewVotePayload struct {
	Value          ReviewVoteValue `json:"value"`
	Reason         string          `json:"reason,omitempty"`
	ContributionID int64           `json:"contribution_id,omitempty"`
	ReviewedSHA    string          `json:"reviewed_sha,omitempty"`
}

// ContributionEventPayload is the JSON shape stored in Event.Payload for the
// contribution_* event kinds.
type ContributionEventPayload struct {
	ContributionID int64  `json:"contribution_id"`
	AgentRole      string `json:"agent_role,omitempty"`
	RefName        string `json:"ref_name,omitempty"`
	HeadSHA        string `json:"head_sha,omitempty"`
	Title          string `json:"title,omitempty"`
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"` // set on contribution_merged
	Reason         string `json:"reason,omitempty"`           // set on contribution_rejected / closed
}

// ReviewVerdict summarises the collective review state for an issue.
type ReviewVerdict string

const (
	ReviewVerdictApproved ReviewVerdict = "approved"
	ReviewVerdictRejected ReviewVerdict = "rejected"
	ReviewVerdictPending  ReviewVerdict = "pending"
)

// EffectiveVote is a single reviewer's latest vote whose head_sha matches
// the issue's current HeadSHA. Stale votes (wrong head_sha) and abstain
// votes are not included in this list.
type EffectiveVote struct {
	Reviewer string          `json:"reviewer"`
	Value    ReviewVoteValue `json:"value"`
	Reason   string          `json:"reason,omitempty"`
	VotedAt  time.Time       `json:"voted_at"`
	IsAgent  bool            `json:"is_agent"`
}

// StaleVote is a reviewer's latest vote that does NOT match the issue's
// current HeadSHA — the reviewer needs to re-review before it counts.
type StaleVote struct {
	Reviewer    string          `json:"reviewer"`
	Value       ReviewVoteValue `json:"value"`
	VotedAt     time.Time       `json:"voted_at"`
	VoteHeadSHA string          `json:"vote_head_sha"`
	IsAgent     bool            `json:"is_agent"`
}

// ReviewStatus is the server-computed review summary. It is the single
// source of truth — the frontend consumes this instead of deriving state
// from the timeline.
type ReviewStatus struct {
	HeadSHA      string          `json:"head_sha"`
	Verdict      ReviewVerdict   `json:"verdict"`
	MergeBlocked bool            `json:"merge_blocked"`
	BlockReason  string          `json:"block_reason,omitempty"`
	Votes        []EffectiveVote `json:"votes"`
	StaleVotes   []StaleVote     `json:"stale_votes"`
	// RequiredReviewers is the set of reviewer role keys that must vote on
	// this contribution (path-matched from the host config, minus the
	// author). PendingReviewers is the subset that has not yet voted — the
	// contribution stays `pending` until this is empty (or a reject lands).
	RequiredReviewers []string `json:"required_reviewers"`
	PendingReviewers  []string `json:"pending_reviewers"`
}

// IssueMergeBlock reports why an issue branch is NOT ready to merge into its
// base, or "" when it is ready. Merge is blocked while any contribution is
// still `pending` (awaiting review — its required reviewers have not all
// voted), and while the issue branch carries no changes (branchAhead false).
//
// Only `pending` blocks: an `approved` contribution the maintainer chose not
// to apply, a `rejected` one, and merged/closed ones are all resolved
// decisions. The branchAhead check ensures at least one contribution was
// applied (the issue branch starts empty, identical to base).
//
// This is the second-level gate; per-contribution review is the first level
// (ComputeContributionReviewStatus).
func IssueMergeBlock(contributions []*Contribution, branchAhead bool) string {
	pending := 0
	for _, c := range contributions {
		if c.Status == ContribStatusPending {
			pending++
		}
	}
	if pending > 0 {
		return fmt.Sprintf("%d contribution(s) still pending review — every contribution must be reviewed (approved/rejected) before merging the issue branch into base", pending)
	}
	if !branchAhead {
		return "issue branch has no changes to merge — it starts empty and only fills as contributions are applied into it; apply at least one approved contribution first"
	}
	return ""
}

// ComputeContributionReviewStatus is the single-source-of-truth review
// computation for a contribution branch (the first-level gate).
//
// requiredReviewers is the set of reviewer role keys that must vote on this
// contribution — path-matched from the host config and with the contribution
// author already removed (a role cannot be required to review its own branch).
//
// The verdict follows the immutable-branch model:
//
//   - rejected: ANY vote on this contribution is `reject` (a reject is
//     dominant — the branch is dead; the author pushes a new version).
//   - approved: no reject, AND every required reviewer has voted
//     (approve/abstain). An empty required set is vacuously approved.
//   - pending:  no reject, but some required reviewer has not voted yet.
//
// Merge into the issue branch is allowed only when approved.
func ComputeContributionReviewStatus(c *Contribution, requiredReviewers []string, events []*Event) *ReviewStatus {
	rs := &ReviewStatus{
		HeadSHA:           c.HeadSHA,
		Verdict:           ReviewVerdictPending,
		MergeBlocked:      true,
		Votes:             []EffectiveVote{},
		StaleVotes:        []StaleVote{},
		RequiredReviewers: append([]string{}, requiredReviewers...),
		PendingReviewers:  []string{},
	}
	if c.HeadSHA == "" {
		rs.BlockReason = "contribution branch has no commits yet"
		return rs
	}

	type latestVote struct {
		event   *Event
		payload ReviewVotePayload
	}
	latest := make(map[string]*latestVote)
	for _, e := range events {
		if e.Kind != EventReviewVote {
			continue
		}
		var p ReviewVotePayload
		if err := jsonUnmarshalPayload(e.Payload, &p); err != nil {
			continue
		}
		if p.ContributionID != c.ID {
			continue
		}
		key := reviewerKey(e)
		if key == "" {
			continue
		}
		if cur, ok := latest[key]; !ok || e.CreatedAt.After(cur.event.CreatedAt) {
			latest[key] = &latestVote{event: e, payload: p}
		}
	}

	// votesByRole indexes effective (current-head) votes by agent role so we
	// can tell which required reviewers have spoken. hasReject is set by any
	// reject vote, from any voter.
	votesByRole := make(map[string]ReviewVoteValue)
	hasReject := false

	keys := make([]string, 0, len(latest))
	for k := range latest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lv := latest[k]
		if lv.payload.ReviewedSHA != "" && lv.payload.ReviewedSHA != c.HeadSHA {
			rs.StaleVotes = append(rs.StaleVotes, StaleVote{
				Reviewer: reviewerDisplay(lv.event), Value: lv.payload.Value,
				VotedAt: lv.event.CreatedAt, VoteHeadSHA: lv.payload.ReviewedSHA, IsAgent: lv.event.AgentRole != "",
			})
			continue
		}
		rs.Votes = append(rs.Votes, EffectiveVote{
			Reviewer: reviewerDisplay(lv.event), Value: lv.payload.Value,
			Reason: lv.payload.Reason, VotedAt: lv.event.CreatedAt, IsAgent: lv.event.AgentRole != "",
		})
		if lv.payload.Value == ReviewVoteReject {
			hasReject = true
		}
		if lv.event.AgentRole != "" {
			votesByRole[lv.event.AgentRole] = lv.payload.Value
		}
	}

	for _, rv := range requiredReviewers {
		if _, voted := votesByRole[rv]; !voted {
			rs.PendingReviewers = append(rs.PendingReviewers, rv)
		}
	}

	switch {
	case hasReject:
		rs.Verdict = ReviewVerdictRejected
		rs.MergeBlocked = true
		rs.BlockReason = "rejected by a reviewer — push a new contribution branch addressing the feedback"
	case len(rs.PendingReviewers) == 0:
		rs.Verdict = ReviewVerdictApproved
		rs.MergeBlocked = false
	default:
		rs.Verdict = ReviewVerdictPending
		rs.MergeBlocked = true
		rs.BlockReason = fmt.Sprintf("waiting for review from: %s", strings.Join(rs.PendingReviewers, ", "))
	}
	return rs
}

// ContributionStatus maps a computed review verdict to the non-terminal
// contribution status to cache on the row. Terminal statuses (merged/closed)
// are decided by the apply/close paths and never produced here.
func (rs *ReviewStatus) ContributionStatus() ContributionStatus {
	switch rs.Verdict {
	case ReviewVerdictApproved:
		return ContribStatusApproved
	case ReviewVerdictRejected:
		return ContribStatusRejected
	default:
		return ContribStatusPending
	}
}

// reviewerKey returns a stable key for a review_vote event's actor: either
// "agent:<agent_role>" or "user:<actor_id>".
func reviewerKey(e *Event) string {
	if e.AgentRole != "" {
		return "agent:" + e.AgentRole
	}
	if e.ActorID > 0 {
		return fmt.Sprintf("user:%d", e.ActorID)
	}
	return ""
}

// reviewerDisplay returns a human-readable label for the reviewer.
func reviewerDisplay(e *Event) string {
	if e.AgentRole != "" {
		return e.AgentRole
	}
	if e.ActorName != "" {
		return e.ActorName
	}
	return fmt.Sprintf("user:%d", e.ActorID)
}

func jsonUnmarshalPayload(data []byte, v any) error {
	if len(data) == 0 {
		return errors.New("empty payload")
	}
	return json.Unmarshal(data, v)
}

// Issue is the metadata row. HeadSHA is the current tip of the issue branch
// (empty until the first push). BaseBranch records the merge target at the
// moment the issue was opened — switching the repo default later does not
// retroactively rebind open issues. ParentID/ParentNumber wire sub-issues:
// the child's base branch is set to the parent's issue/<n> branch at create
// time so merging a child fast-forwards into the parent's branch.
type Issue struct {
	ID         int64
	RepoID     int64
	Number     int64
	AuthorID   int64
	AuthorName string
	// AgentRole is set on agent-created issues. Empty for human-created
	// issues. Mirrors the same field on Comment and Event.
	AgentRole      string
	Title          string
	Body           string
	State          State
	BranchName     string
	BaseBranch     string
	HeadSHA        string
	ParentID       int64
	ParentNumber   int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MergedAt       *time.Time
	MergeCommitSHA string
}

// Comment is a human or agent message attached to an issue. When
// FilePath / Line are non-empty the comment is anchored to a code line —
// rendered inline on the diff tab — otherwise it's a top-level comment on
// the conversation timeline.
//
// Authorship is mutually exclusive:
//
//   - Human comment: AuthorID > 0 (FK into users), AuthorName is the
//     username, AgentRole is empty.
//   - Agent comment: AuthorID == 0 (the DB stores NULL), AuthorName is
//     empty, AgentRole is the host yaml role key (`backend` /
//     `reviewer` / …). The CHECK constraint on the column enforces
//     this XOR at the DB level too.
type Comment struct {
	ID         int64
	IssueID    int64
	AuthorID   int64
	AuthorName string
	AgentRole  string
	Body       string
	FilePath   string
	Line       int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Event is a system-generated timeline entry. Payload carries kind-specific
// fields as JSON (e.g. commit SHAs for EventCommitPushed); handlers decode
// based on Kind.
//
// Attribution is one of three flavours, distinguished by which of
// (ActorID, AgentRole) is set:
//
//   - Human-driven: ActorID > 0, ActorName is the username, AgentRole
//     empty. e.g. a `state_changed open→closed` event triggered by a
//     user click.
//   - Agent-driven: ActorID == 0, ActorName empty, AgentRole is the
//     role key. e.g. a `review_vote` posted by the reviewer role via
//     issue_review_vote.
//   - System (rare): ActorID == 0, both name fields empty. The legacy
//     fallback used when no actor is known.
type Event struct {
	ID        int64
	IssueID   int64
	Kind      EventKind
	Payload   []byte
	ActorID   int64
	ActorName string
	AgentRole string
	CreatedAt time.Time
}

// CommitPushedPayload is the JSON shape stored in Event.Payload for
// EventCommitPushed.
type CommitPushedPayload struct {
	OldSHA  string                `json:"old_sha"`
	NewSHA  string                `json:"new_sha"`
	Commits []CommitPushedSummary `json:"commits"`
}

type CommitPushedSummary struct {
	SHA         string    `json:"sha"`
	Message     string    `json:"message"`
	AuthorName  string    `json:"author_name"`
	CommittedAt time.Time `json:"committed_at"`
}

// BranchMergedPayload is the JSON shape stored in Event.Payload for
// EventBranchMerged. Mode is one of "fast-forward" / "merge-commit" /
// "up-to-date" — mirrors the git domain's MergeBranch return. BaseSHA is
// the commit BaseBranch pointed at immediately *before* the merge, captured
// so post-merge views (e.g. the commits tab) can recover the "commits this
// branch introduced" set after a fast-forward has erased the divergence.
type BranchMergedPayload struct {
	IntoBranch string `json:"into_branch"`
	FromBranch string `json:"from_branch"`
	BaseSHA    string `json:"base_sha,omitempty"`
	MergeSHA   string `json:"merge_sha"`
	Mode       string `json:"mode"`
}

// StateChangedPayload records both sides so the timeline can render arrows.
type StateChangedPayload struct {
	From State `json:"from"`
	To   State `json:"to"`
}

// TitleChangedPayload mirrors StateChangedPayload but for the title string.
type TitleChangedPayload struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ErrIssueNotFound is mapped to 404 in handlers.
var (
	ErrIssueNotFound = errors.New("issue not found")
	ErrInvalidState  = errors.New("invalid issue state transition")
)

// ListFilter narrows GetByRepo. Empty State means "any".
type ListFilter struct {
	State  State
	Offset int32
	Limit  int32
}

// Store is the persistence abstraction. The Postgres impl lives under
// internal/modules/issue/infra.
type Store interface {
	Create(ctx context.Context, repoID, authorID int64, title, body, baseBranch string, agentRole string, parentID, parentNumber int64) (*Issue, error)
	GetByNumber(ctx context.Context, repoID, number int64) (*Issue, error)
	List(ctx context.Context, repoID int64, f ListFilter) ([]*Issue, int64, error)
	ListChildren(ctx context.Context, parentID int64) ([]*Issue, error)
	UpdateTitleBody(ctx context.Context, id int64, title, body string) (*Issue, error)
	UpdateState(ctx context.Context, id int64, state State, mergeSHA string) (*Issue, error)
	UpdateHeadSHA(ctx context.Context, id int64, headSHA string) error
	ListOpenIssueNumbers(ctx context.Context, repoID int64) ([]int64, error)

	CreateComment(ctx context.Context, issueID, authorID int64, body, filePath string, line int) (*Comment, error)
	// CreateAgentComment writes an agent-authored comment row. AuthorID
	// is implicitly NULL on the DB side (the CHECK constraint enforces
	// exactly-one-of with agentRole). agentRole must match the role-key
	// grammar — the service layer validates before calling.
	CreateAgentComment(ctx context.Context, issueID int64, agentRole, body, filePath string, line int) (*Comment, error)
	ListComments(ctx context.Context, issueID int64) ([]*Comment, error)
	GetCommentByID(ctx context.Context, id int64) (*Comment, error)

	CreateEvent(ctx context.Context, issueID int64, kind EventKind, payload []byte, actorID int64) (*Event, error)
	// CreateAgentEvent attributes the event to a host yaml role rather
	// than a user. ActorID is implicitly 0 (DB NULL); the row's
	// agent_role column carries the role key.
	CreateAgentEvent(ctx context.Context, issueID int64, kind EventKind, payload []byte, agentRole string) (*Event, error)
	ListEvents(ctx context.Context, issueID int64) ([]*Event, error)
}

// --- attachments ---

// AttachmentKind classifies the file for frontend rendering decisions.
// Mirrors the DB's kind column.
type AttachmentKind string

const (
	AttachmentKindImage   AttachmentKind = "image"
	AttachmentKindVideo   AttachmentKind = "video"
	AttachmentKindArchive AttachmentKind = "archive"
	AttachmentKindText    AttachmentKind = "text"
	AttachmentKindBinary  AttachmentKind = "binary"
)

// AttachmentStatus tracks the lifecycle of an attachment row.
type AttachmentStatus string

const (
	AttachmentStatusUploaded AttachmentStatus = "uploaded"
	AttachmentStatusAttached AttachmentStatus = "attached"
	AttachmentStatusDeleted  AttachmentStatus = "deleted"
)

// Attachment is the domain model for an uploaded file bound to an issue.
// Rows start as "uploaded" (draft, not yet referenced in a comment) and
// transition to "attached" when a comment body includes the token. Soft
// delete sets status=deleted and wipes the on-disk file; the row stays
// for audit and tombstone rendering on the frontend.
type Attachment struct {
	ID               int64
	RepoID           int64
	IssueID          int64
	CommentID        int64
	AuthorID         int64
	AgentRole        string
	StorageKey       string
	OriginalName     string
	DisplayName      string
	SizeBytes        int64
	MimeType         string
	DetectedMimeType string
	SHA256           string
	Kind             AttachmentKind
	Inline           bool
	Status           AttachmentStatus
	CreatedAt        time.Time
	DeletedAt        *time.Time
}

// AttachmentStore is the persistence abstraction for issue attachments.
type AttachmentStore interface {
	CreateAttachment(ctx context.Context, repoID, issueID, authorID int64, agentRole, storageKey, originalName, displayName string, sizeBytes int64, mimeType, detectedMimeType, sha256 string, kind AttachmentKind, inline bool) (*Attachment, error)
	GetAttachment(ctx context.Context, id int64) (*Attachment, error)
	ListAttachments(ctx context.Context, issueID, commentID int64) ([]*Attachment, error)
	MarkAttached(ctx context.Context, id int64, commentID int64) error
	SoftDeleteAttachment(ctx context.Context, id int64) error
}

// AttachmentUploadParams carries the data the agent_api tool passes
// when uploading an attachment on behalf of an agent session. Data is
// the raw file bytes (decoded from base64 on the server side).
type AttachmentUploadParams struct {
	RepoID      int64
	IssueID     int64
	Data        []byte // raw file bytes
	Name        string // original filename (e.g. "screenshot.png")
	DisplayName string // optional display name override
	Inline      bool
	CommentID   int64
	AgentRole   string
}

// AttachmentUploader is the cross-module seam for uploading attachments
// from the agent_api tool. The issue module's AttachmentService
// implements it; agent_api depends on the interface, not the
// concrete service, so the module boundary stays clean.
type AttachmentUploader interface {
	UploadAttachment(ctx context.Context, params *AttachmentUploadParams) (*Attachment, error)
}

// ---- contributions (branch-based merge requests) ----

// ContributionStatus models the lifecycle of a contribution branch. Status is
// derived from the branch's required reviewers and their votes (see
// ComputeContributionReviewStatus) and cached on the row; it is recomputed on
// push and on every vote.
//
//	pending:   pushed; not all required reviewers have voted yet (default)
//	approved:  every required reviewer voted (approve/abstain), none rejected
//	rejected:  a reviewer rejected the branch (revise via a new versioned branch)
//	merged:    the server merged this branch into the issue branch (terminal)
//	closed:    the owning role abandoned the branch (terminal)
type ContributionStatus string

const (
	ContribStatusPending  ContributionStatus = "pending"
	ContribStatusApproved ContributionStatus = "approved"
	ContribStatusRejected ContributionStatus = "rejected"
	ContribStatusMerged   ContributionStatus = "merged"
	ContribStatusClosed   ContributionStatus = "closed"
)

// Valid reports whether s is one of the documented statuses.
func (s ContributionStatus) Valid() bool {
	switch s {
	case ContribStatusPending, ContribStatusApproved, ContribStatusRejected, ContribStatusMerged, ContribStatusClosed:
		return true
	}
	return false
}

// Terminal reports whether the contribution can no longer change. A rejected
// branch is NOT terminal — a reviewer can revise their vote, and the row stays
// addressable — but it is also never applied while rejected.
func (s ContributionStatus) Terminal() bool {
	return s == ContribStatusMerged || s == ContribStatusClosed
}

// Contribution is one agent's branch in a per-issue namespace
// (refs/heads/issue-<N>/<role>) treated as an independent merge-request.
// Reviews and votes attach to the branch; the server merges approved
// branches into the issue branch. Diff stats are computed from the real
// git diff (DiffMergeBase against the issue branch) at push time.
type Contribution struct {
	ID              int64
	RepoID          int64
	IssueID         int64
	SessionID       int64
	AgentRole       string
	RefName         string // refs/heads/issue-<N>/<role>[/slug]
	HeadSHA         string
	BaseSHA         string // issue head this was last diffed against
	Title           string
	Description     string
	Status          ContributionStatus
	Mergeable       bool
	MergeMode       string // last CheckAutoMerge mode against the issue branch
	ChangedPaths    []string
	Files           int32
	Additions       int32
	Deletions       int32
	MergedCommitSHA string
	MergedAt        *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ContributionUpsertParams carries the post-push snapshot used to create or
// refresh a contribution. The store keys on (IssueID, RefName).
type ContributionUpsertParams struct {
	RepoID       int64
	IssueID      int64
	SessionID    int64
	AgentRole    string
	RefName      string
	HeadSHA      string
	BaseSHA      string
	ChangedPaths []string
	Files        int32
	Additions    int32
	Deletions    int32
}

// ContributionStore is the persistence abstraction for contribution branches.
type ContributionStore interface {
	// UpsertContributionOnPush inserts a contribution for a freshly-pushed
	// namespace ref. New rows start in status 'pending'. Branches are
	// immutable once pushed (the git layer rejects re-pushes), so the
	// ON CONFLICT path only fires on idempotent re-delivery of the same
	// push — it refreshes the diff snapshot but leaves status untouched.
	UpsertContributionOnPush(ctx context.Context, p ContributionUpsertParams) (*Contribution, error)
	GetContribution(ctx context.Context, id int64) (*Contribution, error)
	GetContributionByRef(ctx context.Context, issueID int64, refName string) (*Contribution, error)
	ListContributions(ctx context.Context, issueID int64, includeClosed, includeMerged bool) ([]*Contribution, error)
	SetContributionMeta(ctx context.Context, id int64, title, description string) (*Contribution, error)
	SetContributionStatus(ctx context.Context, id int64, status ContributionStatus) (*Contribution, error)
	SetContributionMergeable(ctx context.Context, id int64, mergeable bool, mode string) error
	// MarkContributionMerged records the server-computed merge commit and
	// flips status to 'merged'.
	MarkContributionMerged(ctx context.Context, id int64, mergedCommitSHA string) (*Contribution, error)
}

var (
	ErrContributionNotFound = errors.New("contribution not found")
)

var (
	ErrAttachmentNotFound = errors.New("attachment not found")
)
