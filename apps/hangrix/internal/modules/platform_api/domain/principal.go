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
// performing the operation. v1 uses a coarse, GitHub-style repo
// permission model: read-only actions are allowed for every authenticated
// actor scoped to the repo; mutating actions require "write".
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
		Permissions: &repoPermissions{write: roleHasWrite(sess)},
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

// repoPermissions implements the coarse read/write repo permission model.
// Read-only actions pass for any authenticated actor; mutating actions
// require write == true.
type repoPermissions struct {
	write bool
}

func (p *repoPermissions) Can(resource string, action string) bool {
	if isReadAction(resource, action) {
		return true
	}
	return p.write
}

// readActions enumerates every (resource:action) pair that only reads
// state. Anything not listed here is treated as a mutation and requires
// write permission — fail-safe, so a newly added write endpoint defaults
// to needing write even if someone forgets to update this set.
var readActions = map[string]struct{}{
	"issues:read":           {},
	"comments:read":         {},
	"todos:list":            {},
	"sessions:list":         {},
	"contributions:list":    {},
	"contributions:read":    {},
	"issues:mergeability":   {},
}

func isReadAction(resource, action string) bool {
	_, ok := readActions[resource+":"+action]
	return ok
}

// roleHasWrite reports whether the session's frozen role_config grants
// "write" repo permission. Anything other than an explicit "write"
// (including a missing field or unparseable snapshot) is treated as
// read-only — fail-safe.
func roleHasWrite(sess *runnerdomain.AgentSession) bool {
	if sess == nil || len(sess.RoleConfig) == 0 {
		return false
	}
	var snap struct {
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(sess.RoleConfig, &snap); err != nil {
		return false
	}
	return snap.Permission == "write"
}
