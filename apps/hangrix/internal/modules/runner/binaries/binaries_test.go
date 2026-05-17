package binaries_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/binaries"
)

// TestEmbeddedRunnerMatchesSHA verifies each embedded runner asset
// reports a SHA matching a freshly-computed hash of its bytes. Catches:
//   - parseAssetName failing to recognise a payload filename,
//   - the cached sha drifting from the bytes it claims to describe.
//
// Skipped when no payload has been built (developer running `go test`
// without first running `npm run embed-binaries`). The skip is correct
// behaviour: tests must not fail just because the operator didn't
// pre-stage embedded artefacts.
func TestEmbeddedRunnerMatchesSHA(t *testing.T) {
	all := binaries.All()
	if len(all) == 0 {
		t.Skip("no runner binaries embedded (run `npm run embed-binaries` first)")
	}
	for _, info := range all {
		t.Run(info.AssetName, func(t *testing.T) {
			sum := sha256.Sum256(info.Bytes)
			want := hex.EncodeToString(sum[:])
			if info.SHA256 != want {
				t.Fatalf("SHA mismatch: info=%q recomputed=%q", info.SHA256, want)
			}
			if info.Size != int64(len(info.Bytes)) {
				t.Fatalf("size mismatch: info=%d len(bytes)=%d", info.Size, len(info.Bytes))
			}
			if info.Name != binaries.Runner {
				t.Fatalf("name=%q, want %q", info.Name, binaries.Runner)
			}
			if info.GOOS == "" || info.GOARCH == "" {
				t.Fatalf("missing GOOS/GOARCH: %+v", info)
			}
		})
	}
}

// TestGetUnknownArchIsNotFound covers the negative path — an arch
// nobody ships for must surface ErrNotEmbedded so the handler can map
// it to 404 cleanly.
func TestGetUnknownArchIsNotFound(t *testing.T) {
	if _, err := binaries.Get(binaries.Runner, "freebsd", "mips"); err == nil {
		t.Fatal("expected ErrNotEmbedded for freebsd/mips, got nil")
	}
}
