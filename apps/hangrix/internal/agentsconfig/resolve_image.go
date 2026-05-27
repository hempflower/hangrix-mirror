package agentsconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// ResolveImageTag picks the docker image tag the runner should use for
// `docker create`. Either container.image (pulled) or a deterministic
// tag the runner will materialise via `docker build`.
//
// The build tag is namespaced by repo id (so two host repos that ship
// the same Dockerfile don't collide in the runner's local image store)
// and content-addressed by a sha over (dockerfile path, context,
// sorted build args). The Dockerfile *contents* are not hashed here —
// docker's own build cache invalidates per-RUN-layer when the
// Dockerfile changes, and using the same tag across edits means the
// last build wins (consistent with how operators expect "rebuild"
// to work).
func ResolveImageTag(repoID int64, c Container) (string, error) {
	if c.Image != "" {
		return c.Image, nil
	}
	if c.Build == nil {
		return "", fmt.Errorf("host yaml: container.image or container.build is required")
	}
	h := sha256.New()
	h.Write([]byte(c.Build.Dockerfile))
	h.Write([]byte{0})
	h.Write([]byte(c.Build.Context))
	h.Write([]byte{0})
	keys := make([]string, 0, len(c.Build.Args))
	for k := range c.Build.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{'='})
		h.Write([]byte(c.Build.Args[k]))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("hangrix-agent-r%d:%s", repoID, hex.EncodeToString(h.Sum(nil))[:12]), nil
}
