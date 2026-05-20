// Package domain declares the LLM-provider registry types and the
// cross-module interfaces other packages depend on.
//
// What this package no longer owns (post agent-identity-token refactor):
//
//   - Session-token issuance / validation. The session token is the
//     in-container agent's *identity*; one is minted per agent_session
//     row in modules/runner. The proxy authenticates via
//     runner/domain.SessionTokenValidator, not anything here.
//
// What this package still owns:
//
//   - The platform's set of registered LLM upstreams (one Provider per
//     vendor + API key + base URL).
//   - Per-provider model allow-lists. The proxy uses these as the routing
//     table: a request body's `model` field is matched against every
//     provider's AllowedModels to pick the upstream that will handle it.
//   - The append-only usage log that the proxy writes to after every
//     round-trip.
package domain

import (
	"context"
	"errors"
	"time"
)

// ProviderType selects the upstream wire-format the proxy translates to.
// v1 ships three; new types are added by extending the proxy's translator
// switch — the domain just stores the tag.
type ProviderType string

const (
	// ProviderTypeOpenAI talks to OpenAI's native Response API directly.
	// No translation. base_url defaults to https://api.openai.com.
	ProviderTypeOpenAI ProviderType = "openai"
	// ProviderTypeAnthropic translates OpenAI Response API <-> Anthropic
	// Messages API.
	ProviderTypeAnthropic ProviderType = "anthropic"
	// ProviderTypeOpenAICompat is OpenAI Response API forwarded as-is to a
	// caller-specified base_url (OpenRouter / vLLM / Together / Groq / ...).
	ProviderTypeOpenAICompat ProviderType = "openai-compat"
)

func (t ProviderType) Valid() bool {
	switch t {
	case ProviderTypeOpenAI, ProviderTypeAnthropic, ProviderTypeOpenAICompat:
		return true
	}
	return false
}

// Provider is one registered upstream. ApiKey is the encrypted form (a
// cryptobox-sealed blob); only the proxy ever decrypts it, and only at
// request-handling time. Handlers never return ApiKey on the wire.
//
// AllowedModels is load-bearing for routing — the proxy resolves an
// incoming `model` to one Provider by scanning this column. A provider
// row with an empty AllowedModels list will never be selected by the
// model-lookup path; admins must enumerate the upstream's models
// explicitly.
type Provider struct {
	ID            int64
	Name          string // [a-z0-9-]{1,64}; appears in admin URLs but not in proxy routes
	Type          ProviderType
	BaseURL       string
	ApiKey        string // sealed blob; opaque to everyone except cryptobox
	AllowedModels []string
	// Disabled flips the row out of routing without deleting it.
	// FindProviderByModel skips disabled rows, so the proxy returns
	// ErrNoModelMatch as if the provider didn't exist.
	Disabled  bool
	CreatedBy int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UsageRecord is one row in the llm_usage_log table, written by the proxy
// after each upstream call. Consumed by M10+ cost dashboards; M6a just
// writes them.
//
// SessionID identifies the calling agent_sessions row when the request
// arrived with a session-token bearer. It's a plain integer (no FK
// across module boundaries) so this module stays decoupled from
// modules/runner.
type UsageRecord struct {
	SessionID        *int64
	ProviderID       int64
	Model            string
	PromptTokens     int32
	CompletionTokens int32
	TotalTokens      int32
	LatencyMS        int32
	StatusCode       int32
	ErrorMessage     string
	RequestPath      string
	RequestBody      string
	ResponseBody     string
}

// Errors.
var (
	ErrProviderNotFound = errors.New("llm provider not found")
	ErrProviderConflict = errors.New("llm provider name already taken")
	ErrInvalidName      = errors.New("invalid llm provider name")
	ErrInvalidProvider  = errors.New("invalid llm provider config")
	ErrNoModelMatch     = errors.New("no provider serves the requested model")
)

// Repo is the persistence abstraction. The Postgres impl in infra/
// satisfies both Repo and Lookup; the module binds the same instance to
// both interfaces.
type Repo interface {
	CreateProvider(ctx context.Context, p *Provider) (*Provider, error)
	GetProviderByName(ctx context.Context, name string) (*Provider, error)
	GetProviderByID(ctx context.Context, id int64) (*Provider, error)
	ListProviders(ctx context.Context) ([]*Provider, error)
	UpdateProvider(ctx context.Context, p *Provider) (*Provider, error)
	SetProviderDisabled(ctx context.Context, id int64, disabled bool) (*Provider, error)
	DeleteProvider(ctx context.Context, id int64) error

	// FindProviderByModel returns the lowest-id provider whose
	// AllowedModels contains `model`. The deterministic ordering means
	// two providers configured for the same model resolve identically
	// across calls (operators can predict which one will serve a given
	// request and override by adjusting allow-lists).
	FindProviderByModel(ctx context.Context, model string) (*Provider, error)

	RecordUsage(ctx context.Context, u *UsageRecord) error
}

// Lookup is the narrow read-only interface the proxy holds. Decoupled
// from Repo so a future read-replica / cache layer can satisfy this
// without reimplementing the write methods.
type Lookup interface {
	FindProviderByModel(ctx context.Context, model string) (*Provider, error)
	RecordUsage(ctx context.Context, u *UsageRecord) error
}
