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
	Addr string `mapstructure:"addr"`
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
	v.SetDefault("database.dsn", "postgres://hangrix:hangrix@localhost:5432/hangrix?sslmode=disable")
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
	v.SetDefault("auth.cookie_name", "hangrix_session")
	v.SetDefault("auth.cookie_secure", false)
	v.SetDefault("auth.session_ttl", "168h") // 7 days
	v.SetDefault("storage.repos_path", "./data/repos")

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
