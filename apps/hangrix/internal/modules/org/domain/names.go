package domain

import "strings"

// reservedNames is the union of system paths and well-known segments that
// must never become user usernames or organization names. Keeping the list
// here (rather than in user/domain) lets both the register handler and the
// org-create handler check against the same source of truth without a
// circular dependency: user.handler imports org/domain anyway via the
// Resolver wired through HandlerDeps.
var reservedNames = map[string]struct{}{
	"_":        {},
	"admin":    {},
	"api":      {},
	"assets":   {},
	"auth":     {},
	"git":      {},
	"healthz":  {},
	"login":    {},
	"logout":   {},
	"me":       {},
	"new":      {},
	"orgs":     {},
	"register": {},
	"repos":    {},
	"settings": {},
	"static":   {},
	"users":    {},
	"hangrix":  {}, // platform-reserved for M7+ official agent owners
}

// IsReservedName reports whether name (case-insensitive) is a reserved owner
// identifier. Callers that mint a user or org row MUST reject reserved
// names with ErrOrgReserved.
func IsReservedName(name string) bool {
	_, ok := reservedNames[strings.ToLower(name)]
	return ok
}
