package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWithEnvSubstitution(t *testing.T) {
	t.Setenv("MY_KEY", "abc123")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	yaml := `
server:
  addr: ":9090"
auth:
  api_key: "${MY_KEY}"
providers:
  openai:
    type: openai-compat
    api_key: "${MY_KEY}"
    base_url: "https://api.openai.com"
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":9090" {
		t.Errorf("addr: %q", cfg.Server.Addr)
	}
	if cfg.Auth.APIKey != "abc123" {
		t.Errorf("auth: %q", cfg.Auth.APIKey)
	}
	openai := cfg.Providers["openai"]
	if openai.Type != "openai-compat" {
		t.Errorf("type: %q", openai.Type)
	}
	if openai.Settings["api_key"] != "abc123" {
		t.Errorf("api_key: %v", openai.Settings["api_key"])
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte("logging:\n  level: warn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr == "" {
		t.Error("default addr not set")
	}
	if cfg.Logging.Level != "warn" {
		t.Errorf("level: %q", cfg.Logging.Level)
	}
}

func TestEnvDefault(t *testing.T) {
	os.Unsetenv("X_DOES_NOT_EXIST")
	got := expand(`addr: "${X_DOES_NOT_EXIST:-fallback}"`)
	want := `addr: "fallback"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadStrictRejectsUnknownTopLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	yaml := `
server:
  addr: ":9090"
bogus_top_level: foo
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("want error for unknown top-level key, got nil")
	}
}

func TestLoadStrictAllowsProviderInlineFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	yaml := `
server:
  addr: ":9090"
providers:
  openai:
    type: openai-compat
    api_key: "sk-test"
    base_url: "https://api.openai.com"
    custom_provider_field: "anything"
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for inline provider fields: %v", err)
	}
	if cfg.Providers["openai"].Settings["custom_provider_field"] != "anything" {
		t.Errorf("inline field not captured: %+v", cfg.Providers["openai"].Settings)
	}
}
