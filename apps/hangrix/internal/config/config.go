package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server ServerConfig `mapstructure:"server"`
}

type ServerConfig struct {
	Addr string `mapstructure:"addr"`
}

// Load reads a YAML config file from path. Env vars with the API_ prefix
// override file values: API_SERVER_ADDR overrides server.addr, etc.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)

	v.SetDefault("server.addr", ":8080")

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
