package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
)

// mockRepo implements the service's repo interface with an in-memory map.
type mockRepo struct {
	mu   sync.Mutex
	data map[string]settingRow
}

type settingRow struct {
	value       string
	description string
}

func newMockRepo() *mockRepo {
	return &mockRepo{data: make(map[string]settingRow)}
}

func (m *mockRepo) GetSetting(_ context.Context, key string) (domain.Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.data[key]
	if !ok {
		return domain.Setting{}, domain.ErrSettingNotFound
	}
	return domain.Setting{Key: key, Value: row.value, Description: row.description}, nil
}

func (m *mockRepo) UpsertSetting(_ context.Context, key, value, description string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = settingRow{value: value, description: description}
	return nil
}

func (m *mockRepo) ListSettings(_ context.Context) ([]domain.Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Setting, 0, len(m.data))
	for k, row := range m.data {
		out = append(out, domain.Setting{Key: k, Value: row.value, Description: row.description})
	}
	return out, nil
}

func newTestService(repo *mockRepo, defs []domain.Definition) *Service {
	return NewService(&ServiceDeps{
		Repo:     repo,
		Registry: domain.NewRegistry(defs),
	})
}

func TestServiceGet_missingKey(t *testing.T) {
	svc := newTestService(newMockRepo(), nil)
	val, found, err := svc.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("found = true, want false")
	}
	if val != "" {
		t.Fatalf("val = %q, want empty", val)
	}
}

func TestServiceGet_hit(t *testing.T) {
	repo := newMockRepo()
	_ = repo.UpsertSetting(context.Background(), "foo", "bar", "")
	svc := newTestService(repo, nil)

	val, found, err := svc.Get(context.Background(), "foo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if val != "bar" {
		t.Fatalf("val = %q, want bar", val)
	}
}

func TestServiceGet_cachesAfterHit(t *testing.T) {
	repo := newMockRepo()
	_ = repo.UpsertSetting(context.Background(), "cached", "v1", "")
	svc := newTestService(repo, nil)

	// First call — populates cache.
	v1, found, err := svc.Get(context.Background(), "cached")
	if err != nil || !found || v1 != "v1" {
		t.Fatalf("first call: val=%q found=%v err=%v", v1, found, err)
	}

	// Modify backend directly (bypassing cache).
	repo.mu.Lock()
	repo.data["cached"] = settingRow{value: "v2"}
	repo.mu.Unlock()

	// Second call should still return v1 (cached).
	v2, found, err := svc.Get(context.Background(), "cached")
	if err != nil || !found || v2 != "v1" {
		t.Fatalf("second call (should be cached): val=%q found=%v err=%v", v2, found, err)
	}
}

func TestServiceGet_negativeCache(t *testing.T) {
	repo := newMockRepo()
	svc := newTestService(repo, nil)

	// First call — missing key, sets negative cache.
	val, found, err := svc.Get(context.Background(), "neg")
	if err != nil || found {
		t.Fatalf("first call: found=%v err=%v val=%q", found, err, val)
	}

	// Insert backend row.
	_ = repo.UpsertSetting(context.Background(), "neg", "val", "")

	// Second call: negative cache exists — returns (cached_empty, true, nil)
	// because the cache entry exists (present=false, value="").
	val, found, err = svc.Get(context.Background(), "neg")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !found {
		t.Fatal("second call: found = false, want true (negative cache is a cache hit)")
	}
	if val != "" {
		t.Fatalf("second call: val = %q, want empty (negative cache value)", val)
	}
}

func TestServiceGetDuration_registeredDefault(t *testing.T) {
	defs := []domain.Definition{
		{Key: "interval", Default: "1h", Description: "reaper interval"},
	}
	svc := newTestService(newMockRepo(), defs)

	d, err := svc.GetDuration(context.Background(), "interval")
	if err != nil {
		t.Fatalf("GetDuration: %v", err)
	}
	if d != time.Hour {
		t.Fatalf("duration = %v, want 1h", d)
	}
}

func TestServiceGetDuration_storedOverridesDefault(t *testing.T) {
	repo := newMockRepo()
	_ = repo.UpsertSetting(context.Background(), "interval", "30m", "")
	defs := []domain.Definition{
		{Key: "interval", Default: "1h", Description: "reaper interval"},
	}
	svc := newTestService(repo, defs)

	d, err := svc.GetDuration(context.Background(), "interval")
	if err != nil {
		t.Fatalf("GetDuration: %v", err)
	}
	if d != 30*time.Minute {
		t.Fatalf("duration = %v, want 30m", d)
	}
}

func TestServiceGetDuration_invalidValue(t *testing.T) {
	repo := newMockRepo()
	_ = repo.UpsertSetting(context.Background(), "interval", "not-a-duration", "")
	defs := []domain.Definition{
		{Key: "interval", Default: "1h", Description: "reaper interval"},
	}
	svc := newTestService(repo, defs)

	_, err := svc.GetDuration(context.Background(), "interval")
	if err == nil {
		t.Fatal("GetDuration expected error for invalid duration, got nil")
	}
}

func TestServiceGetDuration_unregisteredKey(t *testing.T) {
	svc := newTestService(newMockRepo(), nil)
	d, err := svc.GetDuration(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("GetDuration: %v", err)
	}
	if d != 0 {
		t.Fatalf("duration = %v, want 0", d)
	}
}

func TestServiceSet(t *testing.T) {
	repo := newMockRepo()
	svc := newTestService(repo, nil)

	err := svc.Set(context.Background(), "key1", "val1", "test setting")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Verify it persisted.
	row, err := repo.GetSetting(context.Background(), "key1")
	if err != nil {
		t.Fatalf("GetSetting after Set: %v", err)
	}
	if row.Value != "val1" {
		t.Fatalf("Value = %q, want val1", row.Value)
	}
}

func TestServiceSet_invalidatesCache(t *testing.T) {
	repo := newMockRepo()
	_ = repo.UpsertSetting(context.Background(), "key", "old", "")
	svc := newTestService(repo, nil)

	// Populate cache.
	_, _, _ = svc.Get(context.Background(), "key")

	// Set new value.
	_ = svc.Set(context.Background(), "key", "new", "")

	// Read should pick up new value (cache invalidated).
	val, found, err := svc.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if !found {
		t.Fatal("found = false after Set")
	}
	if val != "new" {
		t.Fatalf("val = %q, want new", val)
	}
}

func TestServiceSet_overwrite(t *testing.T) {
	repo := newMockRepo()
	svc := newTestService(repo, nil)

	_ = svc.Set(context.Background(), "k", "v1", "")
	_ = svc.Set(context.Background(), "k", "v2", "")

	val, found, err := svc.Get(context.Background(), "k")
	if err != nil || !found || val != "v2" {
		t.Fatalf("after overwrite: val=%q found=%v err=%v", val, found, err)
	}
}

func TestServiceList(t *testing.T) {
	repo := newMockRepo()
	_ = repo.UpsertSetting(context.Background(), "a", "1", "")
	_ = repo.UpsertSetting(context.Background(), "b", "2", "")
	svc := newTestService(repo, nil)

	rows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
}

func TestServiceList_empty(t *testing.T) {
	svc := newTestService(newMockRepo(), nil)
	rows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("len = %d, want 0", len(rows))
	}
}

// TestServiceStoreInterface verifies Service satisfies domain.Store.
func TestServiceStoreInterface(t *testing.T) {
	svc := newTestService(newMockRepo(), nil)
	var _ domain.Store = svc
}

func TestServiceGet_missingKeyReturnsEmpty(t *testing.T) {
	// Duplicate of TestServiceGet_missingKey — confirms the edge case
	// that a missing key returns ("", false, nil) rather than an error.
	svc := newTestService(newMockRepo(), nil)
	val, found, err := svc.Get(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("found = true for missing key")
	}
	if val != "" {
		t.Fatalf("val = %q, want empty", val)
	}
}
