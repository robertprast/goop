// Package config loads goop's runtime configuration from YAML and the
// environment. The YAML is the source of truth; ${VAR} and ${VAR:-default}
// patterns are substituted from os.Environ before parsing.
package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level runtime configuration.
type Config struct {
	Server    Server              `yaml:"server"`
	Logging   Logging             `yaml:"logging"`
	Auth      Auth                `yaml:"auth"`
	Models    Models              `yaml:"models"`
	Providers map[string]Provider `yaml:"providers"`
}

// Server holds HTTP server settings.
type Server struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// Logging holds slog settings.
type Logging struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // text or json
}

// Auth configures the single shared bearer token. If APIKey is empty, no auth.
type Auth struct {
	APIKey string `yaml:"api_key"`
}

// Models configures the unified /v1/models aggregator.
type Models struct {
	TTL time.Duration `yaml:"ttl"`
}

// Provider is a generic provider config. Concrete fields are decoded by
// each provider's own constructor; this type lets us keep the YAML pluggable.
type Provider struct {
	Type     string         `yaml:"type"`     // "openai", "bedrock", "gemini", "anthropic"
	Disabled bool           `yaml:"disabled"` // skip this provider entirely
	Settings map[string]any `yaml:",inline"`
}

// Load reads, env-substitutes, parses, and validates a YAML config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	expanded := expand(string(raw))

	cfg := defaults()
	dec := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Server: Server{
			Addr:            ":8080",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    0, // 0 = no write timeout (LLM streams can be long)
			IdleTimeout:     120 * time.Second,
			ShutdownTimeout: 10 * time.Second,
		},
		Logging: Logging{Level: "info", Format: "text"},
		Models:  Models{TTL: 10 * time.Minute},
	}
}

// Validate checks server/logging fields and provider URLs are well-formed.
func (c *Config) Validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr is required")
	}
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	case "":
		c.Logging.Level = "info"
	default:
		return fmt.Errorf("logging.level: invalid %q", c.Logging.Level)
	}
	switch strings.ToLower(c.Logging.Format) {
	case "text", "json":
	case "":
		c.Logging.Format = "text"
	default:
		return fmt.Errorf("logging.format: invalid %q", c.Logging.Format)
	}
	for name, p := range c.Providers {
		if p.Type == "" {
			return fmt.Errorf("provider %q: type is required", name)
		}
		if base, ok := p.Settings["base_url"].(string); ok && base != "" {
			if _, err := url.Parse(base); err != nil {
				return fmt.Errorf("provider %q: invalid base_url: %w", name, err)
			}
		}
	}
	return nil
}

// expand replaces ${VAR} and ${VAR:-default} with environment values.
// Unset variables without a default expand to "".
var (
	envWithDefault = regexp.MustCompile(`\$\{(\w+):-([^}]*)\}`)
	envBare        = regexp.MustCompile(`\$\{(\w+)\}`)
)

func expand(s string) string {
	s = envWithDefault.ReplaceAllStringFunc(s, func(m string) string {
		groups := envWithDefault.FindStringSubmatch(m)
		if v := os.Getenv(groups[1]); v != "" {
			return v
		}
		return groups[2]
	})
	s = envBare.ReplaceAllStringFunc(s, func(m string) string {
		groups := envBare.FindStringSubmatch(m)
		return os.Getenv(groups[1])
	})
	return s
}
