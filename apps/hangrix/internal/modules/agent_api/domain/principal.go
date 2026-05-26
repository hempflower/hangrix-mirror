// Package domain declares the agent REST API contract: Actor (the
// authenticated identity derived from the hgxs_ session token or future
// token types), permission model, and cross-cutting types the handler
// layer needs to authorise requests.
package domain

import (
	"encoding/json"

	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// Scope describes the resource scope of the authenticated actor.
type Scope struct {
	RepoID               *int64
	IssueNumber          *int32
	IsCurrentAddressable bool
}

// Actor is the request-scoped identity resolved from a valid bearer token.
// In v1 the SubjectKind is "agent_session" and the token is always hgxs_;
// future versions will add "user_pat", "oauth_app", etc.
//
// Every REST handler (and the legacy tool dispatcher) extracts it from
// the request context via GetActor.
type Actor struct {
	SubjectKind string // v1 = "agent_session"
	SessionID   *int64
	RepoID      *int64
	IssueNumber *int32
	DisplayName string
	RoleKey     string
	Permissions PermissionSet
	RawScope    Scope

	// Session is the full AgentSession row; handlers that need richer
	// fields (RoleConfig for ACL checks, etc.) can pull it from here
	// rather than doing a second DB round-trip. Only populated when
	// SubjectKind == "agent_session".
	Session *runnerdomain.AgentSession
}

// PermissionSet is the access-control contract consumed by authorization
// middleware. Each route handler checks Can(resource, action) before
// performing the operation.
type PermissionSet interface {
	Can(resource string, action string) bool
}

// NewActor derives an Actor from the session row returned by
// SessionTokenValidator.
func NewActor(sess *runnerdomain.AgentSession) *Actor {
	if sess == nil {
		return nil
	}
	var sessionID *int64
	if sess.ID != 0 {
		id := sess.ID
		sessionID = &id
	}
	return &Actor{
		SubjectKind: "agent_session",
		SessionID:   sessionID,
		RepoID:      sess.RepoID,
		IssueNumber: sess.IssueNumber,
		DisplayName: sess.RoleKey,
		RoleKey:     sess.RoleKey,
		Permissions: &sessionPermissions{session: sess},
		RawScope: Scope{
			RepoID:               sess.RepoID,
			IssueNumber:          sess.IssueNumber,
			IsCurrentAddressable: sess.RepoID != nil && sess.IssueNumber != nil,
		},
		Session: sess,
	}
}

// InRepo reports whether the actor is scoped to a repo.
func (a *Actor) InRepo() bool { return a.RepoID != nil }

// InIssue reports whether the actor is scoped to a specific issue.
func (a *Actor) InIssue() bool { return a.RepoID != nil && a.IssueNumber != nil }

// sessionPermissions adapts the legacy tool-name ACL to resource/action checks.
type sessionPermissions struct {
	session *runnerdomain.AgentSession
}

func (p *sessionPermissions) Can(resource string, action string) bool {
	// For v1, map resource+action to legacy tool names.
	// This is a compatibility shim; future versions will use native
	// resource/action ACLs from the host yaml.
	toolName := resourceActionToTool(resource, action)
	if toolName == "" {
		return false
	}
	return canCallTool(p.session, toolName)
}

// resourceActionToTool maps (resource, action) pairs to legacy tool names.
func resourceActionToTool(resource, action string) string {
	mapping := map[string]string{
		"issues:read":          "issue_read",
		"issues:edit":          "issue_edit",
		"issues:close":         "issue_close",
		"issues:merge":         "issue_merge",
		"comments:create":      "issue_comment",
		"comments:read":        "issue_comment_read",
		"todos:list":           "issue_todo_list",
		"todos:update":         "issue_todo_update",
		"contributions:list":   "contribution_list",
		"contributions:read":   "contribution_read",
		"contributions:apply":  "contribution_apply",
		"contributions:close":  "contribution_close",
		"reviews:create":       "issue_review_vote",
		"sessions:list":        "roster_list",
		"sessions:recover":     "session_recover",
		"attachments:create":   "issue_attachment_upload",
		"releases:create":      "release_create",
		"releases:update":      "release_update",
		"releases:delete":      "release_delete",
		"releases:publish":     "release_publish",
	}
	key := resource + ":" + action
	if t, ok := mapping[key]; ok {
		return t
	}
	return ""
}

// canCallTool is a copy of the service.CanCallTool logic to avoid a
// circular dependency between domain and service packages.
func canCallTool(sess *runnerdomain.AgentSession, toolName string) bool {
	if sess == nil || len(sess.RoleConfig) == 0 {
		return false
	}
	// Parse the role config snapshot.
	var snap struct {
		Can []string `json:"can"`
		Not []string `json:"not"`
	}
	if err := json.Unmarshal(sess.RoleConfig, &snap); err != nil {
		return false
	}
	if len(snap.Can) > 0 {
		for _, n := range snap.Can {
			if n == toolName {
				return true
			}
		}
		return false
	}
	if len(snap.Not) > 0 {
		for _, n := range snap.Not {
			if n == toolName {
				return false
			}
		}
		return true
	}
	return false
}
