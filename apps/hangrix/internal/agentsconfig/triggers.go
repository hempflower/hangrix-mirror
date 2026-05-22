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

	// TriggerIssueComment fires for every new comment on an issue. The
	// role's TriggerSpec narrows the response shape via mentioned_only,
	// from_roles, and from_users filters (see CommentFilter). With no
	// filter set the role wakes on every comment — equivalent to the
	// pre-refactor `issue.comment.any`. With `mentioned_only: true`
	// it wakes only when its own key is `@agent-`mentioned — the
	// pre-refactor `issue.comment.mentioned`.
	TriggerIssueComment Trigger = "issue.comment"

	// TriggerCommitPushed fires on a push to any branch the role's
	// scope allows. Reviewer / CI-watcher roles consume this. The
	// role's TriggerSpec accepts `paths` / `paths_ignore` glob lists
	// to narrow which pushes wake it.
	TriggerCommitPushed Trigger = "commit.pushed"

	// TriggerReviewVotePosted fires when a reviewer agent (or human)
	// records an approval / rejection vote.
	TriggerReviewVotePosted Trigger = "review_vote.posted"

	// TriggerCIStatusChanged fires on CI check transitions
	// (queued → running → green/red). Maintainer uses this to gate
	// auto-merge.
	TriggerCIStatusChanged Trigger = "ci.status_changed"

	// TriggerPatchSubmitted fires when an agent submits a patch to the
	// issue. Reviewer / tester / maintainer roles consume this. The
	// role's TriggerSpec accepts `paths` / `paths_ignore` glob lists
	// (same PushFilter) to narrow which patches wake it.
	TriggerPatchSubmitted Trigger = "patch.submitted"
)

// validTriggers is consulted by the parser; map lookup keeps the check
// O(1) and the constants above remain the single source of truth.
var validTriggers = map[Trigger]struct{}{
	TriggerIssueOpened:      {},
	TriggerIssueClosed:      {},
	TriggerIssueComment:     {},
	TriggerCommitPushed:     {},
	TriggerReviewVotePosted: {},
	TriggerCIStatusChanged:  {},
	TriggerPatchSubmitted:   {},
}

// IsValidTrigger reports whether s is a platform-recognised event name.
func IsValidTrigger(s string) bool {
	_, ok := validTriggers[Trigger(s)]
	return ok
}

// TriggerSpec is the per-event filter block parsed from
// `roles.<key>.triggers.<event>:`. The zero value (Comment / Push both
// nil, MentionedOnly false) matches every fire of the event — the same
// behaviour as an empty `{}` mapping in yaml.
//
// Filter applicability is event-scoped — the parser refuses (e.g.)
// `paths:` under `issue.comment` so a typo can't drift into a no-op.
// Inside a single filter block, conditions AND together.
type TriggerSpec struct {
	// Comment is the filter block for the `issue.comment` event. nil
	// for any other event. A non-nil Comment with all fields at zero
	// value (the `{}` case in yaml) wakes on every comment.
	Comment *CommentFilter

	// Push is the filter block for the `commit.pushed` event. nil for
	// any other event.
	Push *PushFilter
}

// CommentFilter narrows which comments wake a role subscribed to
// `issue.comment`. Set fields AND together.
type CommentFilter struct {
	// MentionedOnly, when true, fires the role only when the comment
	// body contains `@agent-<this-role-key>`. Matches the pre-refactor
	// `issue.comment.mentioned` semantics.
	MentionedOnly bool

	// FromRoles, when non-empty, fires the role only for comments
	// posted by an agent whose role key is in this list. Empty slice
	// disables the role-author check.
	FromRoles []string

	// FromUsers, when non-empty, fires the role only for comments
	// posted by a human user whose login is in this list. Empty slice
	// disables the user-author check.
	FromUsers []string
}

// PushFilter narrows which pushes / patches wake a role subscribed to
// `commit.pushed` or `patch.submitted`. Semantics mirror GitHub Actions:
//
//   - Paths: push/patch matches if at least one changed file matches at
//     least one pattern in Paths. Empty = no include gate.
//   - PathsIgnore: push/patch matches if at least one changed file is NOT
//     covered by any pattern in PathsIgnore (a push/patch where every file
//     is ignored is skipped). Empty = no ignore gate.
//
// When both are set, both gates must pass.
type PushFilter struct {
	// Paths are glob patterns (`apps/api/**`, `**/*.go`) the push's
	// changed files are matched against. Standard doublestar semantics
	// (`*` does not cross `/`, `**` does).
	Paths []string

	// PathsIgnore are glob patterns whose matches do NOT count
	// towards "this push affected files this role cares about".
	PathsIgnore []string
}
