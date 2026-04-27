package provider

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/robertprast/goop/internal/config"
)


// BuildAll constructs the configured providers from the loaded config.
// Providers with a missing required secret are silently skipped (with a
// returned warning), so an unset OPENAI_API_KEY just means /openai is
// disabled, not a startup failure.
//
// The returned Warnings slice is human-readable status for the operator;
// use it for logging.
func BuildAll(cfg *config.Config) (providers []Provider, warnings []string, err error) {
	for name, raw := range cfg.Providers {
		if raw.Disabled {
			warnings = append(warnings, fmt.Sprintf("provider %q: disabled by config", name))
			continue
		}
		p, warn, err := build(name, raw)
		if err != nil {
			return nil, warnings, fmt.Errorf("provider %q: %w", name, err)
		}
		if warn != "" {
			warnings = append(warnings, fmt.Sprintf("provider %q: %s", name, warn))
		}
		if p != nil {
			providers = append(providers, p)
		}
	}
	return providers, warnings, nil
}

func build(name string, raw config.Provider) (Provider, string, error) {
	switch raw.Type {
	case "openai-compat":
		return buildOpenAICompat(name, raw)
	case "bedrock":
		return buildBedrockNative(raw)
	case "bedrock-openai":
		return buildBedrockMantle(raw)
	case "gemini":
		return buildGeminiNative(raw)
	case "anthropic":
		return buildAnthropicNative(raw)
	default:
		return nil, "", fmt.Errorf("unknown type %q", raw.Type)
	}
}

func buildOpenAICompat(name string, raw config.Provider) (Provider, string, error) {
	apiKey := getString(raw.Settings, "api_key")
	baseURL := getString(raw.Settings, "base_url")
	prefix := getString(raw.Settings, "prefix")
	if prefix == "" {
		prefix = "/" + name
	}
	if baseURL == "" {
		return nil, "missing base_url", nil
	}
	if apiKey == "" {
		return nil, "missing api_key (skipped)", nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, "", fmt.Errorf("base_url: %w", err)
	}
	// Tolerate base URLs that include a trailing /v1/ — clients always re-add
	// /v1/... in their paths and we don't want a /v1/v1/ collision.
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/v1")

	authHeader := getStringOr(raw.Settings, "auth_header", "Authorization")
	authFmt := getStringOr(raw.Settings, "auth_value_format", "Bearer %s")
	modelsPath := getStringOr(raw.Settings, "models_path", "/v1/models")
	idPrefix := getStringOr(raw.Settings, "model_id_prefix", name+"/")

	cfg := OpenAICompatConfig{
		Name:            name,
		Prefix:          prefix,
		BaseURL:         u,
		AuthHeader:      authHeader,
		AuthValueFormat: authFmt,
		AuthValue:       apiKey,
		ModelsPath:      modelsPath,
		ModelIDPrefix:   idPrefix,
		V1PathOverride:  getString(raw.Settings, "v1_path_override"),
		ExtraHeaders:    getStringMap(raw.Settings, "extra_headers"),
		QueryParams:     getStringMap(raw.Settings, "query_params"),
	}
	p, err := NewOpenAICompat(cfg)
	return p, "", err
}

func buildBedrockNative(raw config.Provider) (Provider, string, error) {
	region := getString(raw.Settings, "region")
	crPrefix := getStringOr(raw.Settings, "cross_region_prefix", "us")
	p, err := NewBedrockNative(BedrockNativeConfig{
		Region:            region,
		CrossRegionPrefix: crPrefix,
	})
	if err != nil {
		// AWS SDK may fail to find creds in homelab; treat as warning.
		return nil, fmt.Sprintf("disabled (%v)", err), nil
	}
	return p, "", nil
}

func buildBedrockMantle(raw config.Provider) (Provider, string, error) {
	region := getString(raw.Settings, "region")
	p, err := NewBedrockMantle(BedrockMantleConfig{Region: region})
	if err != nil {
		return nil, fmt.Sprintf("disabled (%v)", err), nil
	}
	return p, "", nil
}

func buildGeminiNative(raw config.Provider) (Provider, string, error) {
	apiKey := getString(raw.Settings, "api_key")
	if apiKey == "" {
		return nil, "missing api_key (skipped)", nil
	}
	p, err := NewGeminiNative(GeminiNativeConfig{
		APIKey:  apiKey,
		BaseURL: getString(raw.Settings, "base_url"),
	})
	return p, "", err
}

func buildAnthropicNative(raw config.Provider) (Provider, string, error) {
	apiKey := getString(raw.Settings, "api_key")
	if apiKey == "" {
		return nil, "missing api_key (skipped)", nil
	}
	p, err := NewAnthropicNative(AnthropicNativeConfig{
		APIKey:  apiKey,
		Version: getString(raw.Settings, "version"),
		BaseURL: getString(raw.Settings, "base_url"),
	})
	return p, "", err
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringOr(m map[string]any, key, fallback string) string {
	if v := getString(m, key); v != "" {
		return v
	}
	return fallback
}

func getStringMap(m map[string]any, key string) map[string]string {
	raw, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
