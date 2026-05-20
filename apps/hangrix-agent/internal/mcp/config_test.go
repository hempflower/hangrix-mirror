package mcp

import (
	"os"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestLoad_FileNotFound(t *testing.T) {
	cfg, err := Load("/nonexistent/path")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for missing file, got: %+v", cfg)
	}
}

func TestValidate_Stdio(t *testing.T) {
	cfg := &Config{
		McpServers: map[string]ServerDef{
			"test": {Command: "npx", Args: []string{"-y", "foo"}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid stdio config, got: %v", err)
	}
}

func TestValidate_HTTP_MissingURL(t *testing.T) {
	cfg := &Config{
		McpServers: map[string]ServerDef{
			"test": {Type: "http"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for http without url")
	}
}

func TestValidate_HTTP_WithURL(t *testing.T) {
	cfg := &Config{
		McpServers: map[string]ServerDef{
			"test": {Type: "http", URL: "https://example.com/mcp"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid http config, got: %v", err)
	}
}

func TestValidate_SSE_WithURL(t *testing.T) {
	cfg := &Config{
		McpServers: map[string]ServerDef{
			"test": {Type: "sse", URL: "https://example.com/sse"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid sse config, got: %v", err)
	}
}

func TestValidate_UnknownType(t *testing.T) {
	cfg := &Config{
		McpServers: map[string]ServerDef{
			"test": {Type: "grpc"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestValidate_NoCommandOrType(t *testing.T) {
	cfg := &Config{
		McpServers: map[string]ServerDef{
			"test": {},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for server with no command or type")
	}
}

func TestExpandHeaders_Simple(t *testing.T) {
	os.Setenv("TEST_FOO", "bar_value")
	defer os.Unsetenv("TEST_FOO")

	headers := map[string]string{
		"Authorization": "Bearer ${env:TEST_FOO}",
	}
	out, err := ExpandHeaders("test", headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["Authorization"] != "Bearer bar_value" {
		t.Fatalf("expected 'Bearer bar_value', got %q", out["Authorization"])
	}
}

func TestExpandHeaders_MissingVar(t *testing.T) {
	os.Unsetenv("TEST_MISSING")
	headers := map[string]string{
		"X-Token": "${env:TEST_MISSING}",
	}
	_, err := ExpandHeaders("myserver", headers)
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestExpandHeaders_NoVars(t *testing.T) {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	out, err := ExpandHeaders("test", headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["Content-Type"] != "application/json" {
		t.Fatalf("expected passthrough, got %q", out["Content-Type"])
	}
}

func TestExpandHeaders_Nil(t *testing.T) {
	out, err := ExpandHeaders("test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil output for nil input")
	}
}

func TestExpandHeaders_MultipleVars(t *testing.T) {
	os.Setenv("A", "alpha")
	os.Setenv("B", "beta")
	defer os.Unsetenv("A")
	defer os.Unsetenv("B")

	headers := map[string]string{
		"X-A": "${env:A}",
		"X-B": "prefix-${env:B}-suffix",
	}
	out, err := ExpandHeaders("test", headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["X-A"] != "alpha" {
		t.Fatalf("expected 'alpha', got %q", out["X-A"])
	}
	if out["X-B"] != "prefix-beta-suffix" {
		t.Fatalf("expected 'prefix-beta-suffix', got %q", out["X-B"])
	}
}

func TestConvertSchema_Basic(t *testing.T) {
	tool := mcpgo.Tool{
		Name:        "test",
		Description: "a test tool",
	}
	s := convertSchema(tool)
	if s == nil {
		t.Fatal("expected non-nil schema")
	}
	if _, ok := s["type"]; !ok {
		t.Fatal("expected 'type' key in schema")
	}
}
