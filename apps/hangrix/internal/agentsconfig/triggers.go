package agentsconfig

// Trigger is the set of platform events a role can subscribe to. The
// allow-list lives here in domain because the parser, the dispatcher,
// and the audit UI all branch on the same enum — a typo on either side
// would silently drop events.
type Trigger string

const (
	// TriggerIssueOpened fires when a new issue is created. Typically
	// consumed by dispatcher / triage roles.
	TriggerIssueOpened Trigger = "issue.opened"

	// TriggerIssueClosed fires on issue close — used by maintainer /
	// archive roles to drive session archival.
	TriggerIssueClosed Trigger = "issue.closed"

	// TriggerIssueCommentAny fires for every new comment on an issue
	// regardless of mention. Dispatcher subscribes to this to play
	// router; other roles should prefer the targeted
	// `issue.comment.mentioned` form.
	TriggerIssueCommentAny Trigger = "issue.comment.any"

	// TriggerIssueCommentMentioned fires only when the comment body
	// includes `@agent-<role-key>` matching the subscribed role and
	// the comment passes the role's `mention_by` check.
	TriggerIssueCommentMentioned Trigger = "issue.comment.mentioned"

	// TriggerCommitPushed fires on a push to any branch the role's
	// scope allows. Reviewer / CI-watcher roles consume this.
	TriggerCommitPushed Trigger = "commit.pushed"

	// TriggerReviewVotePosted fires when a reviewer agent (or human)
	// records an approval / rejection vote.
	TriggerReviewVotePosted Trigger = "review_vote.posted"

	// TriggerCIStatusChanged fires on CI check transitions
	// (queued → running → green/red). Maintainer uses this to gate
	// auto-merge.
	TriggerCIStatusChanged Trigger = "ci.status_changed"
)

// validTriggers is consulted by the parser; map lookup keeps the check
// O(1) and the constants above remain the single source of truth.
var validTriggers = map[Trigger]struct{}{
	TriggerIssueOpened:           {},
	TriggerIssueClosed:           {},
	TriggerIssueCommentAny:       {},
	TriggerIssueCommentMentioned: {},
	TriggerCommitPushed:          {},
	TriggerReviewVotePosted:      {},
	TriggerCIStatusChanged:       {},
}

// IsValidTrigger reports whether s is a platform-recognised event name.
func IsValidTrigger(s string) bool {
	_, ok := validTriggers[Trigger(s)]
	return ok
}

// MentionBy is the actor-class predicate that gates which `@agent-<role>`
// mentions actually wake the role. The default (when host yaml omits the
// field) is `collaborators`; service/normalize.go applies that AFTER
// validation so absence and explicit value remain distinguishable
// during parsing.
type MentionBy string

const (
	MentionByOwner         MentionBy = "owner"
	MentionByCollaborators MentionBy = "collaborators"
	MentionByAnyone        MentionBy = "anyone"
)

// IsValidMentionBy reports whether s is one of the three allowed
// values. The empty string is NOT valid here — service-layer normalize
// fills the default and the parser only accepts the empty case before
// normalization.
func IsValidMentionBy(s MentionBy) bool {
	switch s {
	case MentionByOwner, MentionByCollaborators, MentionByAnyone:
		return true
	}
	return false
}
