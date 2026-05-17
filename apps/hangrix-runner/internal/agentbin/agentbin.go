// Package agentbin embeds the `hangrix-agent` binary into the runner.
//
// The runner used to download the agent from the server's
// /api/runner/binaries/hangrix-agent endpoint and stash it in a
// content-addressed cache. After the architecture refactor the agent
// rides along with the runner instead: the build pipeline cross-
// compiles the agent for the runner's target GOOS/GOARCH, drops the
// binary into payload/hangrix-agent, and Go embeds it at compile time
// (see apps/hangrix/scripts/build-embed-binaries.mjs).
//
// Extract() writes the embedded bytes to a deterministic on-disk path
// (idempotent — only writes when the destination is missing or has the
// wrong sha) and returns that path. The session orchestrator then bind-
// mounts that path into every agent container, same as before — only
// the source has shifted from "downloaded artefact" to "extracted
// embed".
//
// The agent is built for the SAME (GOOS, GOARCH) as the runner that
// embeds it; both runner variants the project ships are linux/amd64
// and linux/arm64, and the embedded agent matches one-to-one.
package agentbin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// The `all:` prefix includes the .keep / .gitignore placeholders so
// the embed declaration is valid even before the build script stages
// the real agent binary into payload/. ReadFile("payload/hangrix-
// agent") returns fs.ErrNotExist (mapped to ErrNotEmbedded below)
// when no real agent has been staged — that's the local-dev path,
// where running `go test ./apps/hangrix-runner/...` from a fresh
// clone shouldn't require the user to first run the build pipeline.
//
//go:embed all:payload
var payload embed.FS

// agentName is the file name we extract to. Kept as a constant so
// downstream code that wants to compose the path doesn't have to
// hard-code the literal.
const agentName = "hangrix-agent"

// ErrNotEmbedded is returned when the build did not include a real
// agent payload (e.g. local `go build` without first staging the
// binary into payload/). Callers translate this into an actionable
// error since a runner with no agent is unusable.
var ErrNotEmbedded = errors.New("hangrix-agent binary not embedded in runner build")

var (
	once    sync.Once
	bytes_  []byte
	sha     string
	loadErr error
)

func load() {
	once.Do(func() {
		b, err := payload.ReadFile("payload/" + agentName)
		if err != nil {
			loadErr = ErrNotEmbedded
			return
		}
		// An empty file would also indicate a missing embed (some
		// build chains stage a zero-byte placeholder); treat it the
		// same as "not embedded" so the error message is consistent.
		if len(b) == 0 {
			loadErr = ErrNotEmbedded
			return
		}
		sum := sha256.Sum256(b)
		bytes_ = b
		sha = hex.EncodeToString(sum[:])
	})
}

// SHA256 returns the hex-encoded SHA256 of the embedded agent. Used by
// Extract to skip re-writing an already-correct file on disk.
func SHA256() (string, error) {
	load()
	if loadErr != nil {
		return "", loadErr
	}
	return sha, nil
}

// Extract writes the embedded agent to <dir>/hangrix-agent (0o755) if
// the file is missing or its sha doesn't match the embed. Returns the
// full path on success. Idempotent and safe to call on every session
// spawn — typical fast path is "file already there, sha matches, no
// disk write".
func Extract(dir string) (string, error) {
	load()
	if loadErr != nil {
		return "", loadErr
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	dst := filepath.Join(dir, agentName)
	if existing, err := existingSHA(dst); err == nil && existing == sha {
		return dst, nil
	}
	// Atomic replace via tmp + rename so a concurrent process either
	// sees the old binary or the new — never a half-written file.
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, bytes_, 0o755); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename %s -> %s: %w", tmp, dst, err)
	}
	return dst, nil
}

func existingSHA(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
