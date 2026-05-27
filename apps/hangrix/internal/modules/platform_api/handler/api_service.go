package handler

import (
	"context"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
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
	GetMe(ctx context.Context, p *apidomain.Actor) (any, error)

	// Issue
	ReadIssue(ctx context.Context, p *apidomain.Actor) (any, error)
	EditIssue(ctx context.Context, p *apidomain.Actor, title *string, body *string) (any, error)
	ReadIssueByNumber(ctx context.Context, p *apidomain.Actor, issueNumber int64) (any, error)
	CreateIssue(ctx context.Context, p *apidomain.Actor, title, body string, parent bool) (any, error)

	// Comments
	CreateComment(ctx context.Context, p *apidomain.Actor, body, filePath string, line int) (any, error)
	GetComment(ctx context.Context, p *apidomain.Actor, commentID int64) (any, error)

	// Children / Checks
	ListChildren(ctx context.Context, p *apidomain.Actor) (any, error)
	ListChecks(ctx context.Context, p *apidomain.Actor) (any, error)

	// Todos
	ListTodos(ctx context.Context, p *apidomain.Actor) (any, error)
	CreateTodo(ctx context.Context, p *apidomain.Actor, content, status string, position int) (any, error)
	UpdateTodo(ctx context.Context, p *apidomain.Actor, todoID int64, status string, content *string) (any, error)

	// Contributions
	ListContributions(ctx context.Context, p *apidomain.Actor, includeClosed, includeMerged bool) (any, error)
	ReadContribution(ctx context.Context, p *apidomain.Actor, id int64) (any, error)
	SetContributionMeta(ctx context.Context, p *apidomain.Actor, id int64, title, description string) (any, error)
	ApplyContribution(ctx context.Context, p *apidomain.Actor, id int64, message string) (any, error)
	CloseContribution(ctx context.Context, p *apidomain.Actor, id int64, reason string) (any, error)

	// Reviews
	CreateReview(ctx context.Context, p *apidomain.Actor, contributionID int64, value, reason string) (any, error)

	// Merge / Close
	GetMergeability(ctx context.Context, p *apidomain.Actor) (any, error)
	MergeIssue(ctx context.Context, p *apidomain.Actor, message string) (any, error)
	CloseIssue(ctx context.Context, p *apidomain.Actor, reason string) (any, error)

	// Sessions
	ListSessions(ctx context.Context, p *apidomain.Actor) (any, error)
	RecoverSession(ctx context.Context, p *apidomain.Actor, sessionID int64) (any, error)

	// Attachments
	UploadAttachment(ctx context.Context, p *apidomain.Actor, fileBytes []byte, name, displayName string, inline bool, commentID int64) (any, error)

	// Releases
	CreateRelease(ctx context.Context, p *apidomain.Actor, tagName, title, notes string) (any, error)
	UpdateRelease(ctx context.Context, p *apidomain.Actor, id int64, tagName, title, notes *string) (any, error)
	DeleteRelease(ctx context.Context, p *apidomain.Actor, id int64) error
	PublishRelease(ctx context.Context, p *apidomain.Actor, id int64) (any, error)
	UploadReleaseAsset(ctx context.Context, p *apidomain.Actor, releaseID int64, name, contentB64, contentType string) (any, error)

	// Questionnaires
	CreateQuestionnaire(ctx context.Context, p *apidomain.Actor, input apidomain.CreateQuestionnaireInput) (any, error)
	GetQuestionnaire(ctx context.Context, p *apidomain.Actor, id int64) (any, error)
	GetQuestionnaireResult(ctx context.Context, p *apidomain.Actor, id int64) (any, error)
	ListQuestionnaires(ctx context.Context, p *apidomain.Actor) (any, error)
	CloseQuestionnaire(ctx context.Context, p *apidomain.Actor, id int64, reason string) (any, error)
}
