package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewConfig_EnvExpand(t *testing.T) {
	t.Run("AC1: variable expanded in YAML value", func(t *testing.T) {
		t.Setenv("DB_DSN", "postgres://user:pass@host/db")
		path := writeYAML(t, "database:\n  dsn: \"${env:DB_DSN}\"\n")
		cfg, err := NewConfig(path)
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		want := "postgres://user:pass@host/db"
		if cfg.Database.DSN != want {
			t.Errorf("Database.DSN = %q, want %q", cfg.Database.DSN, want)
		}
	})

	t.Run("AC2: missing variable expands to empty string", func(t *testing.T) {
		// Do NOT set NOPE.
		path := writeYAML(t, "redis:\n  password: \"${env:NOPE}\"\n")
		cfg, err := NewConfig(path)
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		if cfg.Redis.Password != "" {
			t.Errorf("Redis.Password = %q, want empty", cfg.Redis.Password)
		}
	})

	t.Run("AC3: variable inside longer string", func(t *testing.T) {
		t.Setenv("DATA_ROOT", "/mnt/data")
		path := writeYAML(t, "storage:\n  repos_path: \"${env:DATA_ROOT}/repos\"\n")
		cfg, err := NewConfig(path)
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		want := "/mnt/data/repos"
		if cfg.Storage.ReposPath != want {
			t.Errorf("Storage.ReposPath = %q, want %q", cfg.Storage.ReposPath, want)
		}
	})

	t.Run("AC4: malformed syntax kept as-is", func(t *testing.T) {
		path := writeYAML(t, "auth:\n  cookie_name: \"${envBROKEN}\"\n")
		cfg, err := NewConfig(path)
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		want := "${envBROKEN}"
		if cfg.Auth.CookieName != want {
			t.Errorf("Auth.CookieName = %q, want %q", cfg.Auth.CookieName, want)
		}
	})

	t.Run("AC5: API_ env override takes priority over YAML expansion", func(t *testing.T) {
		t.Setenv("API_SERVER_ADDR", ":9999")
		// YAML says ${env:NOPE} (which would expand to ""), but the API_
		// env override should win with ":9999".
		path := writeYAML(t, "server:\n  addr: \"${env:NOPE}\"\n")
		cfg, err := NewConfig(path)
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		want := ":9999"
		if cfg.Server.Addr != want {
			t.Errorf("Server.Addr = %q, want %q", cfg.Server.Addr, want)
		}
	})
}

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}
