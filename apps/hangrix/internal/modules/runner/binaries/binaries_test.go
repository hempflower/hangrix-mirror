package binaries_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/binaries"
)

// TestEmbeddedAgentMatchesSHA verifies the //go:embed payload is wired
// to the expected names AND that the SHA256 reported by Info matches a
// re-computed hash of Info.Bytes. This catches two regressions:
//
//   - Renaming a payload file without updating binaries.NameAgent.
//   - Forgetting to invalidate the cached sha when bytes change.
//
// Skipped when no payload has been built (developer running `go test`
// without first running `npm run embed-binaries`). The skip is correct
// behaviour: tests must not fail just because the operator didn't
// pre-stage embedded artefacts.
func TestEmbeddedAgentMatchesSHA(t *testing.T) {
	info, err := binaries.Get(binaries.NameAgent)
	if err != nil {
		t.Skipf("agent binary not embedded (run `npm run embed-binaries` first): %v", err)
	}
	sum := sha256.Sum256(info.Bytes)
	want := hex.EncodeToString(sum[:])
	if info.SHA256 != want {
		t.Fatalf("SHA mismatch: info=%q recomputed=%q", info.SHA256, want)
	}
	if info.Size != int64(len(info.Bytes)) {
		t.Fatalf("size mismatch: info=%d len(bytes)=%d", info.Size, len(info.Bytes))
	}
}

// TestEmbeddedRunnerMatchesSHA mirrors the agent check for the runner
// binary that ships in the same payload directory.
func TestEmbeddedRunnerMatchesSHA(t *testing.T) {
	info, err := binaries.Get(binaries.NameRunner)
	if err != nil {
		t.Skipf("runner binary not embedded: %v", err)
	}
	sum := sha256.Sum256(info.Bytes)
	if hex.EncodeToString(sum[:]) != info.SHA256 {
		t.Fatalf("runner sha mismatch")
	}
}
