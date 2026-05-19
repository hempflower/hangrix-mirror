package kv

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RepoCache wraps the shared Redis client with git-read-cache helpers: key
// construction, JSON serialization, and prefix-based invalidation. All methods
// silently degrade on Redis errors — cache misses never block the caller.
type RepoCache struct {
	client redis.UniversalClient
}

// NewRepoCache constructs a cache backed by the shared Redis client.
func NewRepoCache(client redis.UniversalClient) *RepoCache {
	return &RepoCache{client: client}
}

// Key conventions — every key is "gitcache:{repoID}:{kind}:…" so a single
// SCAN-based invalidation can clear all cache entries for a repo.

// RefKey builds the cache key for the /refs response of a repo.
func RefKey(repoID int64) string {
	return fmt.Sprintf("gitcache:%d:refs", repoID)
}

// TreeViewKey builds the cache key for /tree-view given a ref and path.
func TreeViewKey(repoID int64, ref, path string) string {
	return fmt.Sprintf("gitcache:%d:treeview:%s:%s", repoID, ref, path)
}

// CommitsKey builds the cache key for the first page of /commits.
func CommitsKey(repoID int64, ref string, offset, limit int32) string {
	return fmt.Sprintf("gitcache:%d:commits:%s:%d:%d", repoID, ref, offset, limit)
}

// Get JSON-deserializes the value at key into dest (a non-nil pointer). Returns
// false on cache miss or any Redis / unmarshal error so callers fall through to
// the origin unconditionally.
func (c *RepoCache) Get(ctx context.Context, key string, dest any) bool {
	if c == nil || c.client == nil {
		return false
	}
	data, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		return false
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return false
	}
	return true
}

// Set JSON-serializes value and stores it at key with ttl. Errors are silently
// dropped — cache writes are best-effort.
func (c *RepoCache) Set(ctx context.Context, key string, value any, ttl time.Duration) {
	if c == nil || c.client == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	c.client.Set(ctx, key, data, ttl)
}

// InvalidateRepo deletes every cache key whose prefix matches "gitcache:{repoID}:*".
// Uses SCAN to avoid blocking Redis on large key spaces.
func (c *RepoCache) InvalidateRepo(ctx context.Context, repoID int64) {
	if c == nil || c.client == nil {
		return
	}
	pattern := fmt.Sprintf("gitcache:%d:*", repoID)
	iter := c.client.Scan(ctx, 0, pattern, 0).Iterator()
	for iter.Next(ctx) {
		c.client.Del(ctx, iter.Val())
	}
}
