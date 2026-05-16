package agentsconfig

import (
	"errors"
	"testing"
	"time"

)

func TestParseLockFile_Happy(t *testing.T) {
	t.Parallel()

	body := `
version: 1
agents:
  - ref: hangrix/reviewer@v1.0.0
    resolved_sha: 0123456789abcdef0123456789abcdef01234567
    resolved_at: 2026-05-16T10:00:00Z
  - ref: acme/backend-coder@v0.3.1
    resolved_sha: fedcba9876543210fedcba9876543210fedcba98
    resolved_at: 2026-05-16T10:01:00Z
`
	lf, err := ParseLockFile([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if lf.Version != 1 {
		t.Fatalf("version: %d", lf.Version)
	}
	if len(lf.Agents) != 2 {
		t.Fatalf("agents: %+v", lf.Agents)
	}
	e := lf.Agents["hangrix/reviewer@v1.0.0"]
	if e.ResolvedSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("sha: %q", e.ResolvedSHA)
	}
	if e.ResolvedAt.IsZero() {
		t.Fatalf("resolved_at zero")
	}
}

func TestParseLockFile_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		body   string
		target error
	}{
		{
			name:   "bad-version",
			body:   "version: 9\nagents: []\n",
			target: ErrInvalidVersion,
		},
		{
			name: "missing-ref-suffix",
			body: `
version: 1
agents:
  - ref: hangrix/reviewer
    resolved_sha: 0123456789abcdef0123456789abcdef01234567
    resolved_at: 2026-05-16T10:00:00Z
`,
			target: ErrMissingAgentRef,
		},
		{
			name: "bad-sha-short",
			body: `
version: 1
agents:
  - ref: a/b@v1
    resolved_sha: abc123
    resolved_at: 2026-05-16T10:00:00Z
`,
			target: ErrInvalidLockEntry,
		},
		{
			name: "bad-sha-uppercase",
			body: `
version: 1
agents:
  - ref: a/b@v1
    resolved_sha: 0123456789ABCDEF0123456789abcdef01234567
    resolved_at: 2026-05-16T10:00:00Z
`,
			target: ErrInvalidLockEntry,
		},
		{
			name: "zero-resolved-at",
			body: `
version: 1
agents:
  - ref: a/b@v1
    resolved_sha: 0123456789abcdef0123456789abcdef01234567
    resolved_at: 0001-01-01T00:00:00Z
`,
			target: ErrInvalidLockEntry,
		},
		{
			name: "duplicate-ref",
			body: `
version: 1
agents:
  - ref: a/b@v1
    resolved_sha: 0123456789abcdef0123456789abcdef01234567
    resolved_at: 2026-05-16T10:00:00Z
  - ref: a/b@v1
    resolved_sha: fedcba9876543210fedcba9876543210fedcba98
    resolved_at: 2026-05-16T10:01:00Z
`,
			target: ErrDuplicateLockKey,
		},
		{
			name: "unknown-field",
			body: `
version: 1
agents:
  - ref: a/b@v1
    resolved_sha: 0123456789abcdef0123456789abcdef01234567
    resolved_at: 2026-05-16T10:00:00Z
    extra: 1
`,
			target: ErrUnknownField,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseLockFile([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected err, got nil")
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("got %v, want errors.Is %v", err, tc.target)
			}
		})
	}
}

func TestSerializeLockFile_RoundTripDeterministic(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	lf := &LockFile{
		Version: 1,
		Agents: map[string]LockEntry{
			"zeta/last@v1":   {ResolvedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ResolvedAt: at},
			"alpha/first@v1": {ResolvedSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ResolvedAt: at},
			"mid/m@v1":       {ResolvedSHA: "cccccccccccccccccccccccccccccccccccccccc", ResolvedAt: at},
		},
	}

	got1, err := SerializeLockFile(lf)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	got2, err := SerializeLockFile(lf)
	if err != nil {
		t.Fatalf("serialize 2: %v", err)
	}
	if string(got1) != string(got2) {
		t.Fatalf("serialize not deterministic:\nrun1=%s\nrun2=%s", got1, got2)
	}

	// Round-trip through parser.
	parsed, err := ParseLockFile(got1)
	if err != nil {
		t.Fatalf("re-parse: %v\nbody=%s", err, got1)
	}
	if len(parsed.Agents) != 3 {
		t.Fatalf("re-parse agents: %+v", parsed.Agents)
	}
	for k, want := range lf.Agents {
		got, ok := parsed.Agents[k]
		if !ok {
			t.Fatalf("missing key %q in re-parse", k)
		}
		if got.ResolvedSHA != want.ResolvedSHA {
			t.Fatalf("%q sha drift: got %q want %q", k, got.ResolvedSHA, want.ResolvedSHA)
		}
		if !got.ResolvedAt.Equal(want.ResolvedAt) {
			t.Fatalf("%q at drift: got %v want %v", k, got.ResolvedAt, want.ResolvedAt)
		}
	}
}

func TestSerializeLockFile_RejectsBadEntries(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		lf     *LockFile
		target error
	}{
		{
			name: "bad-version",
			lf: &LockFile{
				Version: 2,
				Agents:  map[string]LockEntry{},
			},
			target: ErrInvalidVersion,
		},
		{
			name: "bad-sha",
			lf: &LockFile{
				Version: 1,
				Agents: map[string]LockEntry{
					"a/b@v1": {ResolvedSHA: "short", ResolvedAt: at},
				},
			},
			target: ErrInvalidLockEntry,
		},
		{
			name: "zero-time",
			lf: &LockFile{
				Version: 1,
				Agents: map[string]LockEntry{
					"a/b@v1": {ResolvedSHA: "0123456789abcdef0123456789abcdef01234567"},
				},
			},
			target: ErrInvalidLockEntry,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SerializeLockFile(tc.lf)
			if err == nil {
				t.Fatalf("expected err")
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("got %v, want errors.Is %v", err, tc.target)
			}
		})
	}
}

func TestLockKey(t *testing.T) {
	t.Parallel()

	ref := AgentRef{Owner: "hangrix", Name: "reviewer", Ref: "v1.0.0"}
	if got := LockKey(ref); got != "hangrix/reviewer@v1.0.0" {
		t.Fatalf("LockKey: %q", got)
	}
}
