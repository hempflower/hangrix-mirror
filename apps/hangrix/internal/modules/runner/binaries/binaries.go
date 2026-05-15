// Package binaries holds the embedded build artefacts the server hands
// to runners (and admins) on demand: the agent binary that goes into
// every container, and the runner binary itself for first-time install.
//
// Files live next to this Go source under ./payload/ and are populated
// by the build pipeline before `go build`. The Makefile target
// `make embed-binaries` cross-compiles both binaries into payload/. The
// payload/ directory is .gitignored — only built artefacts go there.
//
// Why embed rather than serve from a host path? Two reasons:
//  1. One artefact (the server binary) is enough to deploy. No "did you
//     copy the agent binary to /opt/hangrix"-style operator footguns.
//  2. The sha256 advertised over /api/runner/binaries is computed once
//     from the embedded bytes at startup, so it matches the binary the
//     server is actually serving — never a drift between "the file the
//     config points at" and "what the runner downloaded".
package binaries

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sync"
)

//go:embed payload/*
var payload embed.FS

// Known names. Add new entries here when a new binary needs to ship.
const (
	NameAgent  = "hangrix-agent"
	NameRunner = "hangrix-runner"
)

// Info describes one embedded artefact. Bytes is the raw file contents
// (the embedded FS keeps these in memory; we surface them directly so
// the handler can stream without re-opening the embed).
type Info struct {
	Name   string
	Bytes  []byte
	SHA256 string
	Size   int64
}

// ErrNotEmbedded is returned by Get when the requested name has no
// payload — typically because the operator built without first running
// `make embed-binaries`, so payload/ is empty save for .keep.
var ErrNotEmbedded = errors.New("binary not embedded in server build")

var (
	once   sync.Once
	loaded map[string]*Info
	loadErr error
)

func load() {
	once.Do(func() {
		loaded = map[string]*Info{}
		for _, name := range []string{NameAgent, NameRunner} {
			b, err := payload.ReadFile("payload/" + name)
			if err != nil {
				// Missing payload is non-fatal: the server still works
				// for everything that isn't the runner-dispatch path.
				// Record the absence so Get returns ErrNotEmbedded.
				continue
			}
			sum := sha256.Sum256(b)
			loaded[name] = &Info{
				Name:   name,
				Bytes:  b,
				SHA256: hex.EncodeToString(sum[:]),
				Size:   int64(len(b)),
			}
		}
		// Capture an fs.ReadDir error only so dev-time misconfiguration
		// is debuggable; the lookup path still returns ErrNotEmbedded.
		if _, err := fs.ReadDir(payload, "payload"); err != nil {
			loadErr = fmt.Errorf("read embedded payload: %w", err)
		}
	})
}

// Get returns the named artefact. ErrNotEmbedded means the build didn't
// include this binary; any caller serving the file as an HTTP body
// translates that into 404.
func Get(name string) (*Info, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	info, ok := loaded[name]
	if !ok {
		return nil, ErrNotEmbedded
	}
	return info, nil
}

// All returns a snapshot of every embedded binary. Used by the
// /api/runner/binaries listing endpoint.
func All() []*Info {
	load()
	out := make([]*Info, 0, len(loaded))
	for _, v := range loaded {
		out = append(out, v)
	}
	return out
}
