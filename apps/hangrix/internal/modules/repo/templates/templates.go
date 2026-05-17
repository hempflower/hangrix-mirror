// Package templates holds the files seeded into a freshly-created repo
// when the user checks "Initialize repository" on the create form.
//
// The seed is one commit containing README.md + a starter
// `.hangrix/agents.yml` + four role prompts under `.hangrix/prompts/`.
// Users edit `container.image` and `llm.model` after cloning; the
// dispatcher → backend → reviewer → maintainer chain is ready to run as
// soon as those two fields point at real values.
package templates

import (
	"embed"
	"io/fs"
)

//go:embed all:initial
var initialFS embed.FS

// InitialFiles returns the static files seeded into a new repo, keyed by
// repo-relative path. README.md is NOT included here — the caller
// provides the README body (it's templated from the repo's name +
// description, so it can't be a flat asset).
//
// All paths are forward-slash, no leading "./". File contents are read
// from the embedded fs at startup; the returned map is freshly cloned on
// each call so callers can mutate it without affecting other readers.
func InitialFiles() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(initialFS, "initial", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		body, err := initialFS.ReadFile(path)
		if err != nil {
			return err
		}
		// Strip the "initial/" prefix so the key is the repo-relative path.
		rel := path[len("initial/"):]
		out[rel] = body
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
