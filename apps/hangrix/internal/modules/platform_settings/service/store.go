package service

import (
	"context"
	"sync"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/infra"
)

// cacheTTL is how long the in-memory cache is valid before a background
// refresh. 30 seconds means an admin PATCH takes effect within one reaper
// tick (1 hour max) — the PATCH handler also force-invalidates, so it's
// actually immediate.
const cacheTTL = 30 * time.Second

// Store is the caching read-through wrapper around infra.PostgresRepo.
// Reads hit the cache when fresh, otherwise reload from Postgres.
// Writes go through to Postgres then invalidate the cache.
type Store struct {
	repo    *infra.PostgresRepo
	mu      sync.RWMutex
	cache   map[domain.Key]string
	expires time.Time
}

type StoreDeps struct {
	Repo *infra.PostgresRepo
}

func NewStore(deps *StoreDeps) *Store {
	return &Store{
		repo:  deps.Repo,
		cache: make(map[domain.Key]string),
	}
}

// ensureCache reloads the cache from Postgres if it's expired or empty.
func (s *Store) ensureCache(ctx context.Context) error {
	s.mu.RLock()
	if s.cache != nil && time.Now().Before(s.expires) {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	// Double-check after acquiring write lock.
	if s.cache != nil && time.Now().Before(s.expires) {
		return nil
	}

	rows, err := s.repo.ListAll(ctx)
	if err != nil {
		return err
	}
	m := make(map[domain.Key]string, len(rows))
	for _, row := range rows {
		m[row.Key] = row.Value
	}
	s.cache = m
	s.expires = time.Now().Add(cacheTTL)
	return nil
}

// GetDuration reads a duration setting, falling back to the definition default.
func (s *Store) GetDuration(ctx context.Context, key domain.Key) (time.Duration, error) {
	def := domain.DefinitionByKey(key)
	if def == nil {
		return 0, domain.ErrUnknownKey
	}
	if err := s.ensureCache(ctx); err != nil {
		return 0, err
	}
	s.mu.RLock()
	raw, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		raw = def.Default
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		d, _ = time.ParseDuration(def.Default)
	}
	return d, nil
}

// Set validates and persists a setting value.
func (s *Store) Set(ctx context.Context, key domain.Key, value string, updatedBy int64) error {
	def := domain.DefinitionByKey(key)
	if def == nil {
		return domain.ErrUnknownKey
	}
	if def.Validate != nil {
		if err := def.Validate(value); err != nil {
			return err
		}
	}
	var ub *int64
	if updatedBy != 0 {
		ub = &updatedBy
	}
	if err := s.repo.Upsert(ctx, string(key), value, ub); err != nil {
		return err
	}
	s.InvalidateCache()
	return nil
}

// List returns all settings, filling in defaults for keys not yet persisted.
func (s *Store) List(ctx context.Context, prefix string) ([]domain.Setting, error) {
	if err := s.ensureCache(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]domain.Setting, 0, len(domain.Definitions))
	for _, def := range domain.Definitions {
		if prefix != "" && !keyHasPrefix(def.Key, prefix) {
			continue
		}
		val := def.Default
		if cached, ok := s.cache[def.Key]; ok {
			val = cached
		}
		out = append(out, domain.Setting{
			Key:   def.Key,
			Value: val,
		})
	}

	// Also include any persisted keys that aren't in the Definition registry.
	for k := range s.cache {
		if domain.DefinitionByKey(k) != nil {
			continue
		}
		if prefix != "" && !keyHasPrefix(k, prefix) {
			continue
		}
		out = append(out, domain.Setting{
			Key:   k,
			Value: s.cache[k],
		})
	}
	return out, nil
}

// InvalidateCache forces a cache reload on the next read.
func (s *Store) InvalidateCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expires = time.Time{}
}

func keyHasPrefix(k domain.Key, prefix string) bool {
	return len(k) >= len(prefix) && string(k[:len(prefix)]) == prefix
}

// Compile-time check: ensure Store implements domain.Store.
var _ domain.Store = (*Store)(nil)
