// Package service implements the M7b platform MCP tool surface. Each
// tool is a small constructor returning *platformmcpdomain.Tool, bound
// to the domain.ToolProvider interface in module.go. The handler picks
// them all up via the []ToolProvider slice dep.
//
// Per-role `can:` filtering lives in this package too — the helper here
// reads the session's role_config snapshot and intersects with the
// implemented tool set.
package service

import (
	"encoding/json"
	"sort"

	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// RoleCanList returns the role's `can:` whitelist out of the session's
// frozen role_config snapshot. Empty list (nil or zero-length) means
// "the role has no tool ACL" — defensively interpret as "no tools",
// not "all tools" (the operator likely forgot to set can: and we should
// fail closed).
//
// The snapshot shape is the JSON the spawner writes to
// agent_sessions.role_config via buildRoleSnapshot. We only need the
// `can` array here; everything else is ignored.
func RoleCanList(sess *runnerdomain.AgentSession) []string {
	if sess == nil || len(sess.RoleConfig) == 0 {
		return nil
	}
	var snap struct {
		Can []string `json:"can"`
	}
	if err := json.Unmarshal(sess.RoleConfig, &snap); err != nil {
		return nil
	}
	out := append([]string(nil), snap.Can...)
	// Sort so per-role filter operations are deterministic for audit.
	sort.Strings(out)
	return out
}

// CanCallTool reports whether the role permits invoking the named tool.
// Empty `can:` means no tools.
func CanCallTool(sess *runnerdomain.AgentSession, toolName string) bool {
	for _, n := range RoleCanList(sess) {
		if n == toolName {
			return true
		}
	}
	return false
}
