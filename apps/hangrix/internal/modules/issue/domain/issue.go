// Package domain declares the issue model — the M4 unit of work that
// combines a conversation, a git branch, and (in M5) an agent session into a
// single entity. Other modules consume the Store interface via the ioc
// container.
package domain

import (
	"context"
	"errors"
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
	// approve / request_changes / abstain on an issue. The payload
	// follows ReviewVotePayload. Maintainer roles subscribe to the
	// corresponding spawner trigger (review_vote.posted) so they wake
	// when a vote lands.
	EventReviewVote EventKind = "review_vote"
)

// ReviewVoteValue enumerates the three vote outcomes a reviewer (agent or
// human) can record. The string values are stable wire format; do not
// rename without a migration.
type ReviewVoteValue string

const (
	ReviewVoteApprove        ReviewVoteValue = "approve"
	ReviewVoteRequestChanges ReviewVoteValue = "request_changes"
	ReviewVoteAbstain        ReviewVoteValue = "abstain"
)

// Valid reports whether v is one of the three documented values.
func (v ReviewVoteValue) Valid() bool {
	switch v {
	case ReviewVoteApprove, ReviewVoteRequestChanges, ReviewVoteAbstain:
		return true
	}
	return false
}

// ReviewVotePayload is the JSON shape stored in Event.Payload for
// EventReviewVote. Value is the outcome; Reason is the reviewer's free-text
// rationale (always present in the agent path, may be empty in a future
// human path).
type ReviewVotePayload struct {
	Value  ReviewVoteValue `json:"value"`
	Reason string          `json:"reason,omitempty"`
}

// Issue is the metadata row. HeadSHA is the current tip of the issue branch
// (empty until the first push). BaseBranch records the merge target at the
// moment the issue was opened — switching the repo default later does not
// retroactively rebind open issues. ParentID/ParentNumber wire sub-issues:
// the child's base branch is set to the parent's issue/<n> branch at create
// time so merging a child fast-forwards into the parent's branch.
type Issue struct {
	ID             int64
	RepoID         int64
	Number         int64
	AuthorID       int64
	AuthorName     string
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
	State    State
	Offset   int32
	Limit    int32
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
	SizeBytes        int64
	MimeType         string
	DetectedMimeType string
	SHA256           string
	Kind             AttachmentKind
	Status           AttachmentStatus
	CreatedAt        time.Time
	DeletedAt        *time.Time
}

// AttachmentStore is the persistence abstraction for issue attachments.
type AttachmentStore interface {
	CreateAttachment(ctx context.Context, repoID, issueID, authorID int64, agentRole, storageKey, originalName string, sizeBytes int64, mimeType, detectedMimeType, sha256 string, kind AttachmentKind) (*Attachment, error)
	GetAttachment(ctx context.Context, id int64) (*Attachment, error)
	ListAttachments(ctx context.Context, issueID, commentID int64) ([]*Attachment, error)
	MarkAttached(ctx context.Context, id int64, commentID int64) error
	SoftDeleteAttachment(ctx context.Context, id int64) error
}

var (
	ErrAttachmentNotFound = errors.New("attachment not found")
)
