// Package domain declares the LLM-provider registry types and the
// cross-module interfaces other packages depend on. Two consumers exist
// today:
//
//   - modules/llm_provider/handler implements admin CRUD on top of Repo.
//   - modules/llm_proxy reads providers (by name) via Lookup and validates
//     session tokens via Validator. The proxy never touches Postgres
//     directly; it only sees the narrow interfaces here.
//
// Future consumers (M7b platform MCP server, M6c git push) will reuse the
// same Validator and the same `hgxs_` session-token wire format — see
// SessionToken below.
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

// Visibility controls which sessions are allowed to use the provider.
// `platform` providers are usable by any session; `restricted` providers
// are usable only by sessions whose host repo matches one of AllowedRepos
// (glob patterns, evaluated by the proxy at request time — M6a only stores
// the patterns, repo-aware enforcement lands in M6c/M7a).
type Visibility string

const (
	VisibilityPlatform   Visibility = "platform"
	VisibilityRestricted Visibility = "restricted"
)

func (v Visibility) Valid() bool {
	return v == VisibilityPlatform || v == VisibilityRestricted
}

// Provider is one registered upstream. ApiKey is the encrypted form (a
// cryptobox-sealed blob); only the proxy ever decrypts it, and only at
// request-handling time. Handlers never return ApiKey on the wire.
type Provider struct {
	ID                int64
	Name              string // [a-z0-9-]{1,64}; appears in the URL path
	Type              ProviderType
	BaseURL           string
	ApiKey            string // sealed blob; opaque to everyone except cryptobox
	AllowedModels     []string
	Visibility        Visibility
	AllowedRepos      []string
	RateLimitRPM      int32
	IsPlatformDefault bool
	DefaultModel      string
	CreatedBy         int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// SessionToken is the auth artifact the proxy expects in
// `Authorization: Bearer hgxs_<prefix>_<secret>`. M6a ships a test-only
// issuance API; M6c onwards the runner issues these at container start.
// HashedKey is bcrypt(secret); the plaintext is shown once at creation.
type SessionToken struct {
	ID         int64
	Prefix     string
	HashedKey  string
	ProviderID int64
	// Model pins the request body's `model` field. M6a test tokens bind to
	// a single model — the proxy 403's if the request body asks for a
	// different one. Real-agent sessions in M6c may also bind to a model
	// resolved from the role config.
	Model      string
	Label      string
	CreatedBy  int64
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// Active reports whether the token may still be used (not revoked, not
// expired). Mirrors token.Token.Active so callers get consistent semantics.
func (s *SessionToken) Active(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	if s.ExpiresAt != nil && now.After(*s.ExpiresAt) {
		return false
	}
	return true
}

// CreatedSessionToken is returned by Repo.CreateSessionToken. Plaintext is
// the wire-format token (`hgxs_<prefix>_<secret>`) displayed exactly once.
type CreatedSessionToken struct {
	Token     *SessionToken
	Plaintext string
}

// UsageRecord is one row in the llm_usage_log table, written by the proxy
// after each upstream call. Consumed by M10+ cost dashboards; M6a just
// writes them.
type UsageRecord struct {
	SessionTokenID *int64
	ProviderID     int64
	Model          string
	PromptTokens   int32
	CompletionTokens int32
	TotalTokens    int32
	LatencyMS      int32
	StatusCode     int32
	ErrorMessage   string
	RequestPath    string
}

// Validated is what Validator hands back to the proxy after a successful
// token check — the resolved (token, provider) pair so the proxy can route
// to the upstream and enforce model/repo allow-lists.
type Validated struct {
	Token    *SessionToken
	Provider *Provider
}

// Errors.
var (
	ErrProviderNotFound  = errors.New("llm provider not found")
	ErrProviderConflict  = errors.New("llm provider name already taken")
	ErrInvalidName       = errors.New("invalid llm provider name")
	ErrInvalidProvider   = errors.New("invalid llm provider config")
	ErrTokenNotFound     = errors.New("session token not found")
	ErrInvalidToken      = errors.New("invalid session token")
	ErrTokenInactive     = errors.New("session token revoked or expired")
)

// Repo is the persistence abstraction. The Postgres impl in infra/
// satisfies both Repo and Validator (Lookup is identical to GetByName,
// ValidateToken is prefix lookup + bcrypt compare); the module binds the
// same instance to both interfaces.
type Repo interface {
	CreateProvider(ctx context.Context, p *Provider) (*Provider, error)
	GetProviderByName(ctx context.Context, name string) (*Provider, error)
	GetProviderByID(ctx context.Context, id int64) (*Provider, error)
	ListProviders(ctx context.Context) ([]*Provider, error)
	UpdateProvider(ctx context.Context, p *Provider) (*Provider, error)
	DeleteProvider(ctx context.Context, id int64) error
	// SetPlatformDefault flips `is_platform_default` on `id` and clears
	// the flag on every other row in a single transaction so the
	// invariant "at most one platform default" survives concurrent
	// callers.
	SetPlatformDefault(ctx context.Context, id int64) error
	GetPlatformDefault(ctx context.Context) (*Provider, error)

	CreateSessionToken(ctx context.Context, providerID int64, model, label string, createdBy int64, expiresAt *time.Time) (*CreatedSessionToken, error)
	GetSessionTokenByPrefix(ctx context.Context, prefix string) (*SessionToken, error)
	ListSessionTokens(ctx context.Context) ([]*SessionToken, error)
	RevokeSessionToken(ctx context.Context, id int64) error
	TouchSessionTokenLastUsed(ctx context.Context, id int64) error

	RecordUsage(ctx context.Context, u *UsageRecord) error
}

// Lookup is the narrow read-only interface the proxy holds. Decoupled from
// Repo so a future read-replica / cache layer can satisfy this without
// reimplementing the write methods.
type Lookup interface {
	GetProviderByName(ctx context.Context, name string) (*Provider, error)
	RecordUsage(ctx context.Context, u *UsageRecord) error
}

// Validator translates a raw `hgxs_<prefix>_<secret>` plaintext into the
// underlying SessionToken + Provider, or returns ErrInvalidToken /
// ErrTokenInactive. Used by the proxy's Bearer-auth middleware.
type Validator interface {
	ValidateToken(ctx context.Context, plaintext string) (*Validated, error)
}

// SessionTokenWirePrefix is the literal prefix every session-token plaintext
// begins with. Distinct from PATs (`hgx_`) so format alone tells callers
// which validator to route to.
const SessionTokenWirePrefix = "hgxs_"

// SessionTokenPrefixLen is the public-prefix length built into the wire
// format. Same value as token.PrefixLen so operators see consistent
// 8-char identifiers.
const SessionTokenPrefixLen = 8

// SessionTokenSecretLen is the entropy portion length (chars).
const SessionTokenSecretLen = 32
