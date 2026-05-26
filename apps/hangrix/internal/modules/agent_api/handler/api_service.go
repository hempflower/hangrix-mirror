package handler

import (
	"context"

	agentapidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_api/domain"
)

// AgentAPI is the narrow service interface every v1 REST handler depends
// on. It is implemented by service.APIService and wraps the existing
// Registry's business logic with typed parameters and proper error
// returns (instead of the legacy Result{Text, IsError} pattern).
//
// Each method receives the authenticated Actor and carries enough
// context to enforce scope (repo, issue) without repeating the lookup in
// every handler.
type AgentAPI interface {
	// Identity
	GetMe(ctx context.Context, p *agentapidomain.Actor) (any, error)

	// Issue
	ReadIssue(ctx context.Context, p *agentapidomain.Actor) (any, error)
	EditIssue(ctx context.Context, p *agentapidomain.Actor, title *string, body *string) (any, error)
	ReadIssueByNumber(ctx context.Context, p *agentapidomain.Actor, issueNumber int64) (any, error)
	CreateIssue(ctx context.Context, p *agentapidomain.Actor, title, body string, parent bool) (any, error)

	// Comments
	CreateComment(ctx context.Context, p *agentapidomain.Actor, body, filePath string, line int) (any, error)
	GetComment(ctx context.Context, p *agentapidomain.Actor, commentID int64) (any, error)

	// Children / Checks
	ListChildren(ctx context.Context, p *agentapidomain.Actor) (any, error)
	ListChecks(ctx context.Context, p *agentapidomain.Actor) (any, error)

	// Todos
	ListTodos(ctx context.Context, p *agentapidomain.Actor) (any, error)
	CreateTodo(ctx context.Context, p *agentapidomain.Actor, content, status string, position int) (any, error)
	UpdateTodo(ctx context.Context, p *agentapidomain.Actor, todoID int64, status string, content *string) (any, error)

	// Contributions
	ListContributions(ctx context.Context, p *agentapidomain.Actor, includeClosed, includeMerged bool) (any, error)
	ReadContribution(ctx context.Context, p *agentapidomain.Actor, id int64) (any, error)
	SetContributionMeta(ctx context.Context, p *agentapidomain.Actor, id int64, title, description string) (any, error)
	ApplyContribution(ctx context.Context, p *agentapidomain.Actor, id int64, message string) (any, error)
	CloseContribution(ctx context.Context, p *agentapidomain.Actor, id int64, reason string) (any, error)

	// Reviews
	CreateReview(ctx context.Context, p *agentapidomain.Actor, contributionID int64, value, reason string) (any, error)

	// Merge / Close
	GetMergeability(ctx context.Context, p *agentapidomain.Actor) (any, error)
	MergeIssue(ctx context.Context, p *agentapidomain.Actor, message string) (any, error)
	CloseIssue(ctx context.Context, p *agentapidomain.Actor, reason string) (any, error)

	// Sessions
	ListSessions(ctx context.Context, p *agentapidomain.Actor) (any, error)
	RecoverSession(ctx context.Context, p *agentapidomain.Actor, sessionID int64) (any, error)

	// Attachments
	UploadAttachment(ctx context.Context, p *agentapidomain.Actor, fileBytes []byte, name, displayName string, inline bool, commentID int64) (any, error)

	// Releases
	CreateRelease(ctx context.Context, p *agentapidomain.Actor, tagName, title, notes string) (any, error)
	UpdateRelease(ctx context.Context, p *agentapidomain.Actor, id int64, tagName, title, notes *string) (any, error)
	DeleteRelease(ctx context.Context, p *agentapidomain.Actor, id int64) error
	PublishRelease(ctx context.Context, p *agentapidomain.Actor, id int64) (any, error)
	UploadReleaseAsset(ctx context.Context, p *agentapidomain.Actor, releaseID int64, name, contentB64, contentType string) (any, error)
}
