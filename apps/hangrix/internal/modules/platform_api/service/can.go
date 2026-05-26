// Package service implements the agent HTTP API tool surface. Each tool
// is a small constructor returning *apidomain.Tool, bound to
// the domain.ToolProvider interface in module.go. The handler picks
// them all up via the []ToolProvider slice dep.
//
// Per-role `can:` / `not:` filtering lives in this package too — the
// helpers here read the session's role_config snapshot and intersect
// (or exclude) against the implemented tool set.
package service

import (
	"encoding/json"
	"sort"

	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// roleSnap mirrors the JSON shape buildRoleSnapshot writes to
// agent_sessions.role_config. We only need the ACL fields here.
type roleSnap struct {
	Can []string `json:"can"`
	Not []string `json:"not"`
}

func decodeRoleSnap(sess *runnerdomain.AgentSession) *roleSnap {
	if sess == nil || len(sess.RoleConfig) == 0 {
		return nil
	}
	var snap roleSnap
	if err := json.Unmarshal(sess.RoleConfig, &snap); err != nil {
		return nil
	}
	return &snap
}

// RoleCanList returns the role's `can:` whitelist out of the session's
// frozen role_config snapshot. Empty list (nil or zero-length) means
// the role is either blacklist-only (see RoleNotList) or has no tool
// ACL at all — callers should consult RoleNotList before deciding
// whether to fall through to fail-closed.
//
// The snapshot shape is the JSON the spawner writes to
// agent_sessions.role_config via buildRoleSnapshot.
func RoleCanList(sess *runnerdomain.AgentSession) []string {
	snap := decodeRoleSnap(sess)
	if snap == nil {
		return nil
	}
	out := append([]string(nil), snap.Can...)
	sort.Strings(out)
	return out
}

// RoleNotList returns the role's `not:` blacklist. It is only
// consulted when the whitelist is empty — whitelist wins on conflict.
func RoleNotList(sess *runnerdomain.AgentSession) []string {
	snap := decodeRoleSnap(sess)
	if snap == nil {
		return nil
	}
	out := append([]string(nil), snap.Not...)
	sort.Strings(out)
	return out
}

// CanCallTool reports whether the role permits invoking the named
// tool. Resolution rules:
//
//   - Non-empty `can`: whitelist semantics. Only listed tools allowed;
//     `not` is ignored even if set.
//   - Empty `can`, non-empty `not`: blacklist semantics. Every tool is
//     allowed except those listed.
//   - Both empty: fail-closed (no tools).
func CanCallTool(sess *runnerdomain.AgentSession, toolName string) bool {
	snap := decodeRoleSnap(sess)
	if snap == nil {
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
