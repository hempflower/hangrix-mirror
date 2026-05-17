package domain

import "testing"

// IdentityForRole drives the commit author/email used by every push the
// agent makes from inside its container. Per docs/agent-config.md
// §"Identity 与 Audit" the email tail is always `agents.<host-domain>`;
// the role-key is the local part and the commit author name.
func TestIdentityForRoleHTTPURL(t *testing.T) {
	id := IdentityForRole("backend", "https://hangrix.example.com:8443/foo")
	if id.Name != "backend" {
		t.Fatalf("name = %q, want %q", id.Name, "backend")
	}
	want := "backend@agents.hangrix.example.com"
	if id.Email != want {
		t.Fatalf("email = %q, want %q", id.Email, want)
	}
}

func TestIdentityForRoleLocalhostStripsPort(t *testing.T) {
	id := IdentityForRole("reviewer", "http://localhost:8080")
	if id.Email != "reviewer@agents.localhost" {
		t.Fatalf("email = %q, want %q", id.Email, "reviewer@agents.localhost")
	}
}

func TestIdentityForRoleEmptyURLFallsBackToLocal(t *testing.T) {
	id := IdentityForRole("dispatcher", "")
	if id.Email != "dispatcher@agents.local" {
		t.Fatalf("email = %q, want %q", id.Email, "dispatcher@agents.local")
	}
}

func TestIdentityForRoleBareHost(t *testing.T) {
	// A bare host (no scheme) is parsed as a path by url.Parse, so we
	// fall back to the stripPort branch. Guards against config that
	// drops the scheme.
	id := IdentityForRole("maintainer", "hangrix.example")
	if id.Email != "maintainer@agents.hangrix.example" {
		t.Fatalf("email = %q, want %q", id.Email, "maintainer@agents.hangrix.example")
	}
}

func TestIdentityForRoleStripsRoleKeyWhitespace(t *testing.T) {
	// The host yaml parser already trims keys, but a defensive caller
	// (e.g. a future bulk-import tool) might hand us whitespace. The
	// canonical commit-author form must not contain leading/trailing
	// whitespace.
	id := IdentityForRole("  backend  ", "http://localhost:8080")
	if id.Name != "backend" {
		t.Fatalf("name = %q, want %q", id.Name, "backend")
	}
}
