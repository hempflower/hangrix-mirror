package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// getAgentBundle streams the agent repo's tree at <sha> as a deterministic
// tar.gz so the runner can materialise it into its content-addressed cache.
//
// URL: GET /api/runner/agent-bundles/{owner}/{name}/{sha}.tar.gz
// Auth: Bearer hgxr_ (the runner agent token; middleware already gated).
//
// The body is `git archive --format=tar <sha>` piped through gzip with
// timestamps stripped (Header.OS = unknown, ModTime = zero), so the same
// sha produces the same bytes regardless of when the request hits. No
// `--prefix=` wrapping: the tarball unpacks directly into the cache
// directory and the runner mounts that path at /opt/hangrix/bundle.
//
// X-Hangrix-SHA256 is the sha256 of the response body. Because HTTP
// headers come before the body, we buffer the full archive in memory
// before responding — agent repos are tiny (manifest + prompts + maybe
// a README), so buffering is cheap and simpler than HTTP trailers.
//
// Authorisation gates (in order):
//  1. The {owner} must resolve to a real principal.
//  2. The repo must exist under that owner.
//  3. The repo must be classified as an agent (kind=agent) — runners
//     have no business pulling tarballs of non-agent code.
//  4. The {sha} must resolve to a commit in the repo (else 404, not 500,
//     so the runner can fail the session cleanly).
func (h *AgentHandler) getAgentBundle(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "name")
	raw := strings.TrimSpace(chi.URLParam(r, "*"))

	sha, ok := parseBundleTarget(raw)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "agent bundle URL must end in <sha>.tar.gz")
		return
	}
	if !isValidOwner(owner) || !isValidRepoName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid owner or repo name")
		return
	}
	if !isValidSha(sha) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid sha")
		return
	}

	resolved, err := h.orgResolver.ResolveOwner(r.Context(), owner)
	if err != nil {
		// Same 404 for "no such owner" and "no such repo" — don't leak
		// owner-existence to a runner token that shouldn't be probing.
		httpx.WriteError(w, http.StatusNotFound, "agent bundle not found")
		return
	}
	repo, err := h.repos.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(resolved.Kind), resolved.ID, name)
	if err != nil {
		if errors.Is(err, repodomain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "agent bundle not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if repo.Kind != repodomain.KindAgent {
		httpx.WriteError(w, http.StatusNotFound, "agent bundle not found")
		return
	}

	fsPath, err := h.paths.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Reject unreachable sha early so the runner doesn't get a half-stream
	// then have to abort. `cat-file -e` exits 0 iff the object exists.
	check := exec.CommandContext(r.Context(),
		"git",
		"--git-dir="+fsPath,
		"cat-file",
		"-e",
		sha,
	)
	check.Stdout = io.Discard
	check.Stderr = io.Discard
	if err := check.Run(); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "agent bundle not found")
		return
	}

	bundle, err := buildAgentBundle(r.Context(), fsPath, sha)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "build agent bundle: "+err.Error())
		return
	}
	sum := sha256.Sum256(bundle)

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Hangrix-SHA256", hex.EncodeToString(sum[:]))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.tar.gz"`, sha))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // sha-addressed = immutable
	_, _ = w.Write(bundle)
}

// buildAgentBundle runs `git archive --format=tar <sha>` and re-compresses
// the output through a deterministic gzip (no header timestamp, no
// original filename). The two-stage pipe is necessary because the
// gzip emitted by `git archive --format=tar.gz` carries an OS byte +
// mtime that drift across requests — re-encoding ourselves keeps the
// sha256 stable so the runner can use the X-Hangrix-SHA256 header as
// the cache key check.
func buildAgentBundle(ctx context.Context, fsPath, sha string) ([]byte, error) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+fsPath,
		"archive",
		"--format=tar",
		sha,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	tarOut, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git archive %s: %v (%s)", sha, err, strings.TrimSpace(stderr.String()))
	}

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("gzip writer: %w", err)
	}
	// Zero out the optional fields gzip would otherwise embed at write
	// time: ModTime defaults to "now", OS defaults to the runtime OS;
	// either would make the byte stream caller-time-sensitive.
	gz.Name = ""
	gz.Comment = ""
	gz.Extra = nil
	// gz.ModTime zero value is what we want.
	if _, err := gz.Write(tarOut); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

// parseBundleTarget splits "<sha>.tar.gz" into the bare sha. Returns
// ("", false) on any other suffix — the spec calls for tar.gz only.
func parseBundleTarget(raw string) (string, bool) {
	const ext = ".tar.gz"
	if !strings.HasSuffix(raw, ext) {
		return "", false
	}
	return strings.TrimSuffix(raw, ext), true
}

// sha-name / owner-name / repo-name regexes scoped to this file. Each is
// anchored end-to-end; mismatched input returns 400, not 404, so a runner
// with a malformed URL gets actionable feedback instead of "not found".
var (
	shaRe       = regexp.MustCompile(`^[a-f0-9]{4,64}$`) // accept short shas too; git resolves them
	ownerNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)
	repoLocalRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)
)

func isValidSha(s string) bool      { return shaRe.MatchString(s) }
func isValidOwner(s string) bool    { return ownerNameRe.MatchString(s) }
func isValidRepoName(s string) bool { return repoLocalRe.MatchString(s) }
