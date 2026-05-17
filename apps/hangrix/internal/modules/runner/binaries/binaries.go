// Package binaries holds the embedded `hangrix-runner` binaries the
// server hands out — one per (GOOS, GOARCH) pair the project supports.
//
// Wire layout:
//
//	payload/hangrix-runner_linux_amd64
//	payload/hangrix-runner_linux_arm64
//	…
//
// Files are populated by the build pipeline before `go build` (see
// apps/hangrix/scripts/build-embed-binaries.mjs). The payload/ dir is
// .gitignored — only built artefacts go there.
//
// Why embed rather than serve from a host path? Same reasons as before:
//  1. One artefact (the server binary) is enough to deploy — no "did
//     you copy the runner to /opt/hangrix"-style operator footguns.
//  2. The sha256 advertised over /api/runner/binaries is computed once
//     from the embedded bytes at startup, so it matches the binary the
//     server is actually serving — never a drift between "the file the
//     config points at" and "what the runner downloaded".
//
// The agent binary used to live here too. After the M7d cleanup it
// moved into the runner's own embed (apps/hangrix-runner/internal/
// agent_embed/), so the server no longer ships it. The runner extracts
// the agent it shipped with to disk and bind-mounts that into each
// session container — no extra round-trip through the server.
package binaries

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

//go:embed payload/*
var payload embed.FS

// Runner is the canonical wire name of the runner binary; per-platform
// asset filenames are `<Runner>_<goos>_<goarch>`.
const Runner = "hangrix-runner"

// Info describes one embedded artefact. Bytes is the raw file contents
// (the embedded FS keeps these in memory; we surface them directly so
// the handler can stream without re-opening the embed).
type Info struct {
	// Name is the bare binary name (e.g. "hangrix-runner"). Multiple
	// Infos can share the same Name when the binary ships for several
	// platforms — disambiguate with GOOS/GOARCH.
	Name string
	// AssetName is the per-platform filename inside payload/ —
	// `<Name>_<goos>_<goarch>`. This is also the file name the server
	// uses when serving the binary so a curl downloads land with a
	// useful default filename.
	AssetName string
	GOOS      string
	GOARCH    string
	Bytes     []byte
	SHA256    string
	Size      int64
}

// ErrNotEmbedded is returned when no embedded artefact matches the
// lookup — typically because the operator built without first running
// `npm run embed-binaries`, so payload/ is empty save for .keep, OR
// because the requested arch isn't one we ship.
var ErrNotEmbedded = errors.New("binary not embedded in server build")

var (
	once    sync.Once
	loaded  []*Info
	loadErr error
)

func load() {
	once.Do(func() {
		entries, err := fs.ReadDir(payload, "payload")
		if err != nil {
			loadErr = fmt.Errorf("read embedded payload: %w", err)
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			// Skip the placeholder files that keep the directory alive
			// in git when no real binaries have been staged.
			if name == ".keep" || name == ".gitignore" || strings.HasPrefix(name, ".") {
				continue
			}
			bare, goos, goarch, ok := parseAssetName(name)
			if !ok {
				// Unrecognised payload — ignore rather than crash the
				// server. Operator can fix the build script.
				continue
			}
			b, err := payload.ReadFile("payload/" + name)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(b)
			loaded = append(loaded, &Info{
				Name:      bare,
				AssetName: name,
				GOOS:      goos,
				GOARCH:    goarch,
				Bytes:     b,
				SHA256:    hex.EncodeToString(sum[:]),
				Size:      int64(len(b)),
			})
		}
		// Deterministic order for /api/runner/binaries listings.
		sort.Slice(loaded, func(i, j int) bool {
			return loaded[i].AssetName < loaded[j].AssetName
		})
	})
}

// parseAssetName splits `<name>_<goos>_<goarch>` into its parts.
// Returns ok=false when the filename doesn't match the expected
// triplet so the loader can skip it.
func parseAssetName(s string) (name, goos, goarch string, ok bool) {
	// Split from the right because name itself may contain '_' on
	// future binaries; for "hangrix-runner_linux_amd64" the right-most
	// two segments are goos / goarch.
	i := strings.LastIndexByte(s, '_')
	if i <= 0 {
		return "", "", "", false
	}
	j := strings.LastIndexByte(s[:i], '_')
	if j <= 0 {
		return "", "", "", false
	}
	name = s[:j]
	goos = s[j+1 : i]
	goarch = s[i+1:]
	if name == "" || goos == "" || goarch == "" {
		return "", "", "", false
	}
	return name, goos, goarch, true
}

// Get looks up an artefact by (name, goos, goarch). ErrNotEmbedded
// means either the build didn't include the binary or no variant
// matches the requested platform; any caller serving the file as an
// HTTP body translates that into 404.
func Get(name, goos, goarch string) (*Info, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	for _, info := range loaded {
		if info.Name == name && info.GOOS == goos && info.GOARCH == goarch {
			return info, nil
		}
	}
	return nil, ErrNotEmbedded
}

// GetByAssetName looks up an artefact by its per-platform filename
// (e.g. "hangrix-runner_linux_amd64"). Used by the binary-serving
// handler so the URL path can carry the asset name verbatim.
func GetByAssetName(asset string) (*Info, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	for _, info := range loaded {
		if info.AssetName == asset {
			return info, nil
		}
	}
	return nil, ErrNotEmbedded
}

// All returns a snapshot of every embedded binary. Used by the
// /api/runner/binaries listing endpoint.
func All() []*Info {
	load()
	out := make([]*Info, len(loaded))
	copy(out, loaded)
	return out
}
