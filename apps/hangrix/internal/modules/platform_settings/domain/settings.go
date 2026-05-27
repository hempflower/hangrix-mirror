// Package domain declares the platform_settings module's external contract:
// a key-value store for runtime-mutable, platform-wide configuration knobs.
// Settings are cached in-memory with a 30s TTL; the cache is force-refreshed
// on PATCH so admin changes take effect on the next read.
//
// Keys are typed (duration, int, bool, string) and validated at write time
// against a central registry of Definitions.
package domain

import (
	"context"
	"errors"
	"time"
)

// Key is a dot-separated setting path, e.g. "lifecycle.idle_stop_threshold".
type Key string

// Kind is the wire type of the setting value.
type Kind string

const (
	KindDuration Kind = "duration"
	KindInt      Kind = "int"
	KindBool     Kind = "bool"
	KindString   Kind = "string"
)

// Setting is one persisted key-value pair.
type Setting struct {
	Key       Key
	Value     string
	UpdatedAt time.Time
	UpdatedBy *int64
}

// Definition is a registered key with type, default, and optional validation.
type Definition struct {
	Key      Key
	Kind     Kind
	Default  string
	Validate func(string) error
}

// Store is the read/write interface for platform settings. The service
// layer adds caching; consumers (Reaper, admin handlers) depend on this
// interface via the ioc container.
type Store interface {
	// GetDuration reads a duration-typed setting, falling back to the
	// registered Definition.Default when the key is absent or unparseable.
	GetDuration(ctx context.Context, key Key) (time.Duration, error)

	// Set writes a string value for key, validated against the registered
	// Definition. updatedBy is the user who made the change (0 for system).
	Set(ctx context.Context, key Key, value string, updatedBy int64) error

	// List returns every setting whose key starts with prefix (or all
	// settings when prefix is empty), including keys that only have
	// defaults (not yet persisted).
	List(ctx context.Context, prefix string) ([]Setting, error)

	// InvalidateCache forces a cache reload on the next read. Called from
	// the PATCH handler so admin changes take effect immediately.
	InvalidateCache()
}

// ErrUnknownKey is returned when Set is called with a key not in the registry.
var ErrUnknownKey = errors.New("unknown platform setting key")

// ErrInvalidValue is returned when the value fails the Definition.Validate check.
var ErrInvalidValue = errors.New("invalid value for platform setting")
