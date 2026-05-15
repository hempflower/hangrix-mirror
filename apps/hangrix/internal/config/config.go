package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	Auth     AuthConfig     `mapstructure:"auth"`
	Storage  StorageConfig  `mapstructure:"storage"`
	LLM      LLMConfig      `mapstructure:"llm"`
	Runner   RunnerConfig   `mapstructure:"runner"`
}

// RunnerConfig holds M6c runner-orchestration settings.
//
// The agent + runner binaries are //go:embed'd into the server image, so
// there's no file-path knob to set. The MCP endpoint is also derived
// from server.url at request time (so a URL change in config naturally
// rolls forward to every newly dispatched session). The only knob worth
// exposing today is DefaultAgentImage — the container image the runner
// pulls when a session doesn't pin one. M7a starts driving this per-role
// from host repo .hangrix/agents.yml.
type RunnerConfig struct {
	DefaultAgentImage string `mapstructure:"default_agent_image"`
}

// LLMConfig holds platform-wide LLM settings — currently just the AES-256
// master key used by modules/llm_provider to encrypt provider api keys at
// rest. Per-provider details (base_url / api_key / allowed_models) live in
// the database, not here.
type LLMConfig struct {
	// EncryptionKey is a base64-encoded 32-byte key required to seal and
	// unseal provider api keys. The llm_provider module panics at startup
	// if it is unset or malformed.
	EncryptionKey string `mapstructure:"encryption_key"`
}

type StorageConfig struct {
	// ReposPath is the directory under which bare repositories live, as
	// `<ReposPath>/<owner>/<name>.git`. Created on demand at first use.
	ReposPath string `mapstructure:"repos_path"`
}

type ServerConfig struct {
	// Addr is the bind address — what http.ListenAndServe sees.
	Addr string `mapstructure:"addr"`
	// URL is the externally-reachable base URL of the platform (no
	// trailing slash). Used wherever the server needs to emit an
	// absolute URL — runner bootstrap, in-container agent endpoints,
	// future webhook callbacks. Defaults to "http://localhost<Addr>"
	// when unset; production deployments MUST set it.
	URL string `mapstructure:"url"`
}

type DatabaseConfig struct {
	DSN string `mapstructure:"dsn"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type AuthConfig struct {
	CookieName   string        `mapstructure:"cookie_name"`
	CookieSecure bool          `mapstructure:"cookie_secure"`
	SessionTTL   time.Duration `mapstructure:"session_ttl"`
}

// NewConfig reads a YAML config file from path. Env vars with the API_ prefix
// override file values: API_SERVER_ADDR overrides server.addr, etc.
func NewConfig(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)

	v.SetDefault("server.addr", ":8080")
	v.SetDefault("server.url", "")
	v.SetDefault("database.dsn", "postgres://hangrix:hangrix@localhost:5432/hangrix?sslmode=disable")
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
	v.SetDefault("auth.cookie_name", "hangrix_session")
	v.SetDefault("auth.cookie_secure", false)
	v.SetDefault("auth.session_ttl", "168h") // 7 days
	v.SetDefault("storage.repos_path", "./data/repos")
	v.SetDefault("runner.default_agent_image", "")

	v.SetEnvPrefix("API")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &c, nil
}
