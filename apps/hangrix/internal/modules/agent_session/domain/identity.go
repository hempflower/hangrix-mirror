package domain

import (
	"net/url"
	"strings"
)

// CommitIdentity is the (name, email) pair every agent push uses on the
// host repo. Per docs/agent-config.md §"Identity 与 Audit":
//
//	commit author name  = role key
//	commit author email = <role-key>@agents.<host-domain>
//
// receive-pack accepts these without complaint (git itself doesn't
// validate authorship); the platform-side audit row records (role_key,
// session_id) alongside so a commit can be traced back to the row that
// produced it even if someone forges the author locally.
type CommitIdentity struct {
	Name  string
	Email string
}

// IdentityForRole derives the canonical (name, email) for an agent
// session given the role key and the platform's externally-reachable
// URL. hostURL is parsed for its host portion; the scheme / port /
// path are ignored so a config that points at
// "http://localhost:8080/foo" still yields "@agents.localhost". An
// unparseable hostURL falls back to "agents.local" — emails stay
// well-formed and the audit row's role_key column remains the
// authoritative identifier either way.
//
// The function is pure so the runner module's session-spawn path can
// call it without an ioc binding (it lives in domain rather than
// service for the same reason).
func IdentityForRole(roleKey, hostURL string) CommitIdentity {
	roleKey = strings.TrimSpace(roleKey)
	domain := commitDomainFromURL(hostURL)
	return CommitIdentity{
		Name:  roleKey,
		Email: roleKey + "@agents." + domain,
	}
}

func commitDomainFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "local"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// Treat raw as a bare host string (e.g. "hangrix.example").
		if raw == "" {
			return "local"
		}
		return stripPort(raw)
	}
	return stripPort(u.Host)
}

func stripPort(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return "local"
	}
	return host
}
