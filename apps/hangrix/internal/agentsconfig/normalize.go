package agentsconfig


// NormalizeHostConfig fills schema-level defaults on a HostConfig that
// already passed ParseHostConfig. Two-pass shape is intentional: the
// parser surfaces the operator's literal yaml (so "absent" stays
// distinguishable from "explicit default"), and this pass applies the
// platform defaults the dispatcher relies on. Idempotent — calling it
// twice produces the same result as once.
//
// Defaults applied:
//   - role.mention_by: "" -> "collaborators" (the spec default).
func NormalizeHostConfig(cfg *HostConfig) {
	for _, role := range cfg.Roles {
		if role.MentionBy == "" {
			role.MentionBy = MentionByCollaborators
		}
	}
}
