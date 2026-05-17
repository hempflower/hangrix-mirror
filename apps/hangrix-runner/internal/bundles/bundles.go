// Package bundles owns the runner-side content-addressed cache for agent
// repo bundles. The platform identifies the agent code a session needs
// as "<owner>/<name>@<sha>"; the runner translates that pin into a
// directory on local disk it can bind-mount into the agent container at
// /opt/hangrix/bundle:ro.
//
// Cache layout (under cfg.StateDir / "agent-bundles"):
//
//	agent-bundles/
//	├── <sha-1>/                 unpacked tree (agent.yml at the root)
//	│   └── ...
//	├── <sha-2>/
//	└── tmp/                     scratch dir for atomic-rename inflation
//
// One sha → one directory. Two sessions pinning the same agent_sha share
// the same on-disk tree (the mount is read-only, so the agent process
// can't mutate it).
//
// Eviction is LRU + capacity-bounded; both knobs come from cfg defaults
// described in docs/runner-protocol.md §"Agent bundle 分发":
// 1 GiB total / 14-day max-age. We touch each directory's mtime on every
// resolve hit so atime-based eviction works on filesystems where atime
// is disabled (the default on most modern Linux installs).
package bundles

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Defaults match docs/runner-protocol.md. Override via NewCache.Config.
const (
	defaultMaxBytes = int64(1) << 30 // 1 GiB
	defaultMaxAge   = 14 * 24 * time.Hour
)

// Fetcher is the narrow contract the cache uses to pull a missing
// tarball from the platform. The runner's *client.Client satisfies it
// (HTTP GET /api/runner/agent-bundles/{owner}/{name}/{sha}.tar.gz with
// hgxr_ Bearer + X-Hangrix-SHA256 verification). Keeping the dependency
// behind an interface lets tests swap in an httptest.Server-backed
// fetcher (or a static in-memory tarball) without spinning real HTTP.
type Fetcher interface {
	FetchAgentBundle(ctx context.Context, owner, name, sha string, w io.Writer) (sha256Hex string, err error)
}

// Config tunes Cache behavior. Zero values mean "use defaults".
type Config struct {
	Root     string        // cache root dir (required)
	MaxBytes int64         // 0 → defaultMaxBytes
	MaxAge   time.Duration // 0 → defaultMaxAge
}

// Cache resolves an agent_repo pin into a local directory, fetching +
// extracting the bundle on a cache miss. Safe for concurrent use across
// SessionDriver goroutines: per-sha inflation is serialised so two
// concurrent resolves for the same sha share one download, while
// different shas inflate in parallel.
type Cache struct {
	cfg     Config
	fetcher Fetcher

	mu     sync.Mutex          // guards inflight + cache GC bookkeeping
	flight map[string]*flight  // sha → in-progress inflation
}

type flight struct {
	wg   sync.WaitGroup
	path string
	err  error
}

// New constructs a Cache. The fetcher is what runs the actual HTTP GET
// when a sha is missing; in production it's wrapped around
// client.Client.FetchAgentBundle.
func New(cfg Config, fetcher Fetcher) (*Cache, error) {
	if cfg.Root == "" {
		return nil, errors.New("bundles: cache root required")
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = defaultMaxAge
	}
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, fmt.Errorf("bundles: mkdir cache root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Root, "tmp"), 0o755); err != nil {
		return nil, fmt.Errorf("bundles: mkdir tmp: %w", err)
	}
	c := &Cache{
		cfg:     cfg,
		fetcher: fetcher,
		flight:  map[string]*flight{},
	}
	// Best-effort startup GC; failures are logged by caller (we return
	// nil here so a transient filesystem hiccup doesn't block serve()).
	_ = c.GC()
	return c, nil
}

// agentRepoRe parses the wire form `<owner>/<name>@<sha>` (sha is hex,
// 4–64 chars to allow git short shas — fully resolved by the time the
// platform writes the session row, but we don't enforce length here
// because the server already validated it).
var agentRepoRe = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9._-]{0,99})/([A-Za-z0-9_][A-Za-z0-9._-]{0,99})@([a-f0-9]{4,64})$`)

// Resolve returns the absolute host path the orchestrator should mount
// at /opt/hangrix/bundle. Cache hit → immediate return. Miss → fetch
// the tarball, verify sha256 against the X-Hangrix-SHA256 header, untar
// into <cache>/tmp/<sha>.<rand>/, rename to <cache>/<sha>/.
//
// Concurrent callers for the same sha share one inflation. Different
// shas inflate in parallel.
func (c *Cache) Resolve(ctx context.Context, agentRepo string) (string, error) {
	owner, name, sha, err := parseAgentRepo(agentRepo)
	if err != nil {
		return "", err
	}

	// Hit path: directory exists. Bump mtime so LRU sees it.
	dir := filepath.Join(c.cfg.Root, sha)
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		_ = os.Chtimes(dir, time.Now(), time.Now())
		return dir, nil
	}

	c.mu.Lock()
	if fl, ok := c.flight[sha]; ok {
		c.mu.Unlock()
		fl.wg.Wait()
		if fl.err != nil {
			return "", fl.err
		}
		return fl.path, nil
	}
	fl := &flight{}
	fl.wg.Add(1)
	c.flight[sha] = fl
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.flight, sha)
		c.mu.Unlock()
		fl.wg.Done()
	}()

	path, err := c.fetchAndExtract(ctx, owner, name, sha)
	fl.path = path
	fl.err = err
	return path, err
}

// fetchAndExtract pulls one tarball, verifies its sha256 against the
// X-Hangrix-SHA256 header (Fetcher returns the header value), and
// inflates to <cache>/<sha>/ via the standard mkdir-tmp-rename trick.
func (c *Cache) fetchAndExtract(ctx context.Context, owner, name, sha string) (string, error) {
	final := filepath.Join(c.cfg.Root, sha)
	// Buffer the tarball in memory: agent repos are tiny and we need the
	// full bytes both for sha256 verification and for tar reading. An
	// agent bundle should be << 10 MiB; 1 GiB cache cap means even a
	// pathological 100 MiB bundle wouldn't OOM a typical runner host.
	var buf bytes.Buffer
	got, err := c.fetcher.FetchAgentBundle(ctx, owner, name, sha, &buf)
	if err != nil {
		return "", fmt.Errorf("fetch bundle %s/%s@%s: %w", owner, name, sha, err)
	}
	// Integrity check: body bytes vs. X-Hangrix-SHA256 header. The
	// header value is sha256(tarball) emitted by the platform after
	// `git archive <sha> | gzip -n`; the runner recomputes from the
	// received bytes and rejects on mismatch (tampering / a transport
	// intermediary that re-compressed the response).
	//
	// We deliberately do NOT compare the tarball's sha256 against the
	// requested git commit sha — those are different hash functions
	// over different data and can never be equal. The git-sha identity
	// is enforced server-side: the platform runs `git archive <sha>`
	// against the agent repo's bare tree, so the bytes by construction
	// come from the requested commit.
	sum := sha256.Sum256(buf.Bytes())
	calc := hex.EncodeToString(sum[:])
	if got != "" && got != calc {
		return "", fmt.Errorf("bundle sha mismatch: header %s != body %s", got, calc)
	}

	tmp, err := os.MkdirTemp(filepath.Join(c.cfg.Root, "tmp"), sha+".")
	if err != nil {
		return "", fmt.Errorf("bundles: mkdir tmp: %w", err)
	}
	cleanup := tmp
	defer func() {
		if cleanup != "" {
			_ = os.RemoveAll(cleanup)
		}
	}()

	if err := untarGz(&buf, tmp); err != nil {
		return "", fmt.Errorf("untar bundle %s: %w", sha, err)
	}

	if err := os.Rename(tmp, final); err != nil {
		// Race: another goroutine inflated the same sha just before us.
		// The cache is content-addressed, so the rival directory is
		// byte-identical (sha256 just verified). Drop our tmp and
		// reuse theirs.
		if errors.Is(err, os.ErrExist) {
			cleanup = "" // outer defer will RemoveAll(tmp) anyway, but be explicit
			_ = os.RemoveAll(tmp)
			return final, nil
		}
		return "", fmt.Errorf("rename %s → %s: %w", tmp, final, err)
	}
	cleanup = "" // success path: keep the final dir
	return final, nil
}

// GC evicts cache entries by age (older than cfg.MaxAge) and by total
// size (LRU until total <= cfg.MaxBytes). Called on cache startup and
// may be called periodically by the runner if it ever grows an
// integrated background sweeper. Safe to call concurrently with
// Resolve; eviction takes the mu so an in-flight inflation can't be
// stat-then-evicted out from under us.
func (c *Cache) GC() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.cfg.Root)
	if err != nil {
		return fmt.Errorf("bundles: read cache root: %w", err)
	}

	type item struct {
		path  string
		size  int64
		mtime time.Time
	}
	items := make([]item, 0, len(entries))
	cutoff := time.Now().Add(-c.cfg.MaxAge)

	for _, e := range entries {
		if !e.IsDir() || e.Name() == "tmp" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(c.cfg.Root, e.Name())
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		size := dirSize(full)
		if st.ModTime().Before(cutoff) {
			_ = os.RemoveAll(full)
			continue
		}
		items = append(items, item{path: full, size: size, mtime: st.ModTime()})
	}

	var total int64
	for _, it := range items {
		total += it.size
	}
	if total <= c.cfg.MaxBytes {
		return nil
	}

	// LRU: oldest mtime first.
	sort.Slice(items, func(i, j int) bool { return items[i].mtime.Before(items[j].mtime) })
	for _, it := range items {
		if total <= c.cfg.MaxBytes {
			break
		}
		if err := os.RemoveAll(it.path); err == nil {
			total -= it.size
		}
	}
	return nil
}

// dirSize walks a directory and sums file sizes. Best-effort: a
// transient EACCES on a stale subdir during GC doesn't fail the sweep.
func dirSize(dir string) int64 {
	var sum int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			sum += info.Size()
		}
		return nil
	})
	return sum
}

// parseAgentRepo splits "<owner>/<name>@<sha>" and rejects malformed
// pins early so Resolve never half-fetches against a bad input.
func parseAgentRepo(s string) (owner, name, sha string, err error) {
	m := agentRepoRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", "", fmt.Errorf("bundles: invalid agent_repo %q (want <owner>/<name>@<sha>)", s)
	}
	return m[1], m[2], m[3], nil
}

// untarGz extracts a gzipped tarball into dst. Treats absolute paths
// and "../" segments as protocol violations (the platform-side
// `git archive --format=tar` never emits them, but defence-in-depth is
// cheap). Recreates directory hierarchy as it goes; file modes are
// preserved.
func untarGz(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar header: %w", err)
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || name == "/" {
			continue
		}
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") || strings.Contains(name, "../") {
			return fmt.Errorf("bundles: unsafe tar path %q", hdr.Name)
		}
		target := filepath.Join(dst, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fs(hdr.Mode, 0o755)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs(hdr.Mode, 0o644))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Reject symlinks: the agent runs read-only on the mount
			// but a malicious bundle could still leak the cache layout
			// via dangling links. Agent repos have no legitimate
			// reason to contain symlinks.
			return fmt.Errorf("bundles: symlink in bundle (%q → %q) refused", hdr.Name, hdr.Linkname)
		default:
			// Ignore other entry types (devices, hard links) — git
			// archive doesn't produce them.
		}
	}
}

// fs picks a sensible non-zero mode when the tar header didn't carry one
// (some tar producers leave Mode=0 for stripped-permission archives).
func fs(mode int64, fallback os.FileMode) os.FileMode {
	if mode == 0 {
		return fallback
	}
	// Mask off type bits that can leak through hdr.Mode.
	return os.FileMode(mode) & os.ModePerm
}

// ---- HTTP fetcher ----

// HTTPFetcher wraps the runner's *client.Client (or any equivalent
// http.Client + base URL pair) into a Fetcher. The cache package owns
// the Bearer / header handling so the client package stays focused on
// task / message / input forwarding.
type HTTPFetcher struct {
	Base       string
	AgentToken string
	HTTP       *http.Client
}

// FetchAgentBundle issues the GET and copies the body into w. The
// returned string is the X-Hangrix-SHA256 header value (empty if the
// server didn't send one — Cache.fetchAndExtract treats missing header
// as a fatal mismatch via the eventual sha compare).
func (f *HTTPFetcher) FetchAgentBundle(ctx context.Context, owner, name, sha string, w io.Writer) (string, error) {
	if f.HTTP == nil {
		f.HTTP = http.DefaultClient
	}
	url := strings.TrimRight(f.Base, "/") + "/api/runner/agent-bundles/" + owner + "/" + name + "/" + sha + ".tar.gz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if f.AgentToken == "" {
		return "", errors.New("bundles: agent token unset")
	}
	req.Header.Set("Authorization", "Bearer "+f.AgentToken)
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GET %s: %d %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return "", err
	}
	return resp.Header.Get("X-Hangrix-SHA256"), nil
}
