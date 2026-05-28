package domain

import (
	"context"
	"testing"
	"time"
)

func TestNewRegistry(t *testing.T) {
	defs := []Definition{
		{Key: "max_session_ttl", Default: "168h", Description: "Max session TTL"},
		{Key: "reaper_interval", Default: "1h", Description: "Reaper wake interval"},
	}
	r := NewRegistry(defs)
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
}

func TestRegistryLookup_found(t *testing.T) {
	defs := []Definition{
		{Key: "max_session_ttl", Default: "168h", Description: "Max session TTL"},
	}
	r := NewRegistry(defs)

	d, ok := r.Lookup("max_session_ttl")
	if !ok {
		t.Fatal("Lookup returned false for registered key")
	}
	if d.Default != "168h" {
		t.Fatalf("Default = %q, want 168h", d.Default)
	}
	if d.Description != "Max session TTL" {
		t.Fatalf("Description = %q, want Max session TTL", d.Description)
	}
}

func TestRegistryLookup_missing(t *testing.T) {
	r := NewRegistry(nil)
	_, ok := r.Lookup("nonexistent")
	if ok {
		t.Fatal("Lookup returned true for unregistered key")
	}
}

func TestRegistryLookup_emptyKey(t *testing.T) {
	r := NewRegistry([]Definition{
		{Key: "valid", Default: "1h", Description: "valid"},
	})
	_, ok := r.Lookup("")
	if ok {
		t.Fatal("Lookup returned true for empty key")
	}
}

func TestRegistryDuplicateKey(t *testing.T) {
	defs := []Definition{
		{Key: "dup", Default: "first", Description: "first"},
		{Key: "dup", Default: "second", Description: "second"},
	}
	r := NewRegistry(defs)
	d, ok := r.Lookup("dup")
	if !ok {
		t.Fatal("Lookup returned false for dup key")
	}
	// Last definition wins in map iteration — this is intentional.
	if d.Default != "second" {
		t.Fatalf("Default = %q, want second (last wins)", d.Default)
	}
}

func TestSettingTimestamps(t *testing.T) {
	now := time.Now()
	s := Setting{
		Key:         "test",
		Value:       "42",
		Description: "test setting",
		UpdatedAt:   now,
	}
	if s.Key != "test" || s.Value != "42" {
		t.Fatal("Setting fields not preserved")
	}
	if s.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero")
	}
}

func TestStoreInterfaceCompiles(t *testing.T) {
	// Compile-time check that the interface is well-formed. A nil
	// pointer satisfies this in tests; real implementations are wired
	// through the ioc container.
	var _ Store = (*mockStore)(nil)
}

// mockStore is a no-op implementation used only for the interface
// assertion above. Real unit tests of the service layer use an
// in-memory mock repo.
type mockStore struct{}

func (m *mockStore) Get(_ context.Context, _ string) (string, bool, error)          { return "", false, nil }
func (m *mockStore) GetDuration(_ context.Context, _ string) (time.Duration, error) { return 0, nil }
func (m *mockStore) Set(_ context.Context, _, _, _ string) error                    { return nil }
func (m *mockStore) List(_ context.Context) ([]Setting, error)                      { return nil, nil }
