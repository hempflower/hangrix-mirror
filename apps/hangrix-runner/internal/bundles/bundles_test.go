package bundles_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/bundles"
)

// fakeFetcher serves deterministic in-memory tarballs and counts hits so
// tests can verify caching + single-flight behaviour without spinning a
// real HTTP server.
//
// When headerOverride is set, it's returned verbatim as the
// X-Hangrix-SHA256 value instead of sha256(body) — used to simulate a
// dishonest / mangled response.
type fakeFetcher struct {
	mu             sync.Mutex
	calls          atomic.Int32
	bodies         map[string][]byte // sha → tar.gz bytes
	pause          chan struct{}     // optional gate to test concurrent resolves
	releaser       sync.Once
	headerOverride string
}

func (f *fakeFetcher) FetchAgentBundle(ctx context.Context, owner, name, sha string, w io.Writer) (string, error) {
	f.calls.Add(1)
	f.mu.Lock()
	body := f.bodies[sha]
	override := f.headerOverride
	f.mu.Unlock()
	if f.pause != nil {
		<-f.pause
	}
	if _, err := w.Write(body); err != nil {
		return "", err
	}
	if override != "" {
		return override, nil
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// mkTarGz builds a deterministic gzipped tarball with a fixed entry set
// so the same `files` map always yields the same sha256.
func mkTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	gz.Name = "" // deterministic
	tw := tar.NewWriter(gz)
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	// Deterministic order
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, name := range keys {
		body := files[name]
		hdr := &tar.Header{
			Name:    name,
			Mode:    0o644,
			Size:    int64(len(body)),
			ModTime: time.Unix(0, 0),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func TestResolveHitMissAndContents(t *testing.T) {
	dir := t.TempDir()
	tar := mkTarGz(t, map[string]string{
		"agent.yml":         "version: 1\nkind: agent\nentry:\n  base_prompt: prompts/system.md\n",
		"prompts/system.md": "You are a helpful agent.\n",
	})
	sum := sha256.Sum256(tar)
	sha := hex.EncodeToString(sum[:])

	ff := &fakeFetcher{bodies: map[string][]byte{sha: tar}}
	c, err := bundles.New(bundles.Config{Root: dir}, ff)
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	repo := "owner/name@" + sha
	path1, err := c.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if got := ff.calls.Load(); got != 1 {
		t.Fatalf("expected 1 fetch, got %d", got)
	}
	if filepath.Base(path1) != sha {
		t.Fatalf("path %q not under sha dir", path1)
	}
	body, err := os.ReadFile(filepath.Join(path1, "agent.yml"))
	if err != nil {
		t.Fatalf("read agent.yml: %v", err)
	}
	if !strings.Contains(string(body), "kind: agent") {
		t.Fatalf("agent.yml content not extracted: %q", body)
	}

	// Second resolve: cache hit. No additional fetch.
	path2, err := c.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if path1 != path2 {
		t.Fatalf("cache hit returned different path: %q vs %q", path1, path2)
	}
	if got := ff.calls.Load(); got != 1 {
		t.Fatalf("cache hit triggered extra fetch (got %d)", got)
	}
}

func TestResolveSingleFlight(t *testing.T) {
	dir := t.TempDir()
	tar := mkTarGz(t, map[string]string{"agent.yml": "version: 1\nkind: agent\nentry:\n  base_prompt: p.md\n"})
	sum := sha256.Sum256(tar)
	sha := hex.EncodeToString(sum[:])

	gate := make(chan struct{})
	ff := &fakeFetcher{bodies: map[string][]byte{sha: tar}, pause: gate}
	c, err := bundles.New(bundles.Config{Root: dir}, ff)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	const N = 8
	repo := "owner/name@" + sha
	var wg sync.WaitGroup
	results := make([]string, N)
	errs := make([]error, N)
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := c.Resolve(context.Background(), repo)
			results[i], errs[i] = p, err
		}(i)
	}
	// Wait briefly so all goroutines are inside Resolve waiting on the
	// inflight slot, then let the fetcher proceed.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := ff.calls.Load(); got != 1 {
		t.Fatalf("expected single-flight to coalesce to 1 fetch, got %d", got)
	}
	for i, e := range errs {
		if e != nil {
			t.Fatalf("resolve[%d]: %v", i, e)
		}
		if results[i] != results[0] {
			t.Fatalf("resolve[%d] = %q, want %q", i, results[i], results[0])
		}
	}
}

func TestResolveRejectsInvalidPin(t *testing.T) {
	c, err := bundles.New(bundles.Config{Root: t.TempDir()}, &fakeFetcher{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for _, pin := range []string{
		"",
		"owner/name",                          // missing @sha
		"owner/name@xyz",                      // non-hex sha
		"@deadbeef",                           // missing owner/name
		"owner@deadbeef",                      // missing name
		"owner/name@deadbeefdeadbeefdeadbe2g", // non-hex char
	} {
		if _, err := c.Resolve(context.Background(), pin); err == nil {
			t.Errorf("Resolve(%q) accepted invalid pin", pin)
		}
	}
}

// TestResolveDetectsShaMismatch covers the defence-in-depth check that
// the runner runs against the X-Hangrix-SHA256 header. The git-commit
// sha and the tarball sha256 are different hash functions, so the
// runner doesn't compare body bytes against the URL-path sha (the
// server's `git archive <sha>` already authoritatively pins identity).
// What we DO catch: a server / transport intermediary that emits a
// header whose value disagrees with the body bytes.
func TestResolveDetectsShaMismatch(t *testing.T) {
	dir := t.TempDir()
	// Pin is a synthetic git-sha-shaped value; the body is a tarball whose
	// own sha256 is unrelated. The fake fetcher returns a deliberately
	// wrong header so the runner's body↔header check trips.
	pin := strings.Repeat("ab", 20) // 40 hex chars
	tar := mkTarGz(t, map[string]string{"a.txt": "A"})
	ff := &fakeFetcher{
		bodies:         map[string][]byte{pin: tar},
		headerOverride: strings.Repeat("cd", 32), // 64 hex chars, mismatching
	}
	c, err := bundles.New(bundles.Config{Root: dir}, ff)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	_, err = c.Resolve(context.Background(), "owner/name@"+pin)
	if err == nil || !strings.Contains(err.Error(), "sha mismatch") {
		t.Fatalf("expected sha mismatch error, got %v", err)
	}
}
