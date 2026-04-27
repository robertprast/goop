package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// AnthropicNativeConfig configures the native Anthropic Messages API
// passthrough at /anthropic. Clients use the anthropic SDK; goop injects
// x-api-key and anthropic-version headers.
type AnthropicNativeConfig struct {
	APIKey  string
	Version string // anthropic-version header; default "2023-06-01"
	BaseURL string // optional override; default https://api.anthropic.com
}

// NewAnthropicNative builds the /anthropic passthrough.
func NewAnthropicNative(cfg AnthropicNativeConfig) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("anthropic: api_key is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("anthropic: invalid base_url: %w", err)
	}
	version := cfg.Version
	if version == "" {
		version = "2023-06-01"
	}
	return &anthropicNative{
		apiKey:  cfg.APIKey,
		version: version,
		base:    u,
		client:  &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type anthropicNative struct {
	apiKey  string
	version string
	base    *url.URL
	client  *http.Client
}

func (p *anthropicNative) Name() string                 { return "anthropic" }
func (p *anthropicNative) Prefix() string               { return "/anthropic" }
func (p *anthropicNative) Transport() http.RoundTripper { return nil }

func (p *anthropicNative) Rewrite(pr *httputil.ProxyRequest) {
	stripClientAuth(pr.Out.Header)

	pr.SetURL(p.base)
	pr.SetXForwarded()

	rest := strings.TrimPrefix(pr.In.URL.Path, "/anthropic")
	pr.Out.URL.Path = singleSlash(p.base.Path + rest)
	pr.Out.URL.RawPath = ""

	pr.Out.Header.Set("x-api-key", p.apiKey)
	pr.Out.Header.Set("anthropic-version", p.version)
}

func (p *anthropicNative) ListModels(ctx context.Context) ([]Model, error) {
	endpoint := *p.base
	endpoint.Path = singleSlash(p.base.Path + "/v1/models")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", p.version)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic: list models: HTTP %d", resp.StatusCode)
	}

	var body struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(body.Data))
	now := time.Now().Unix()
	for _, m := range body.Data {
		out = append(out, Model{
			ID:       "anthropic/" + m.ID,
			Object:   "model",
			Created:  now,
			OwnedBy:  "anthropic",
			Provider: "anthropic",
		})
	}
	return out, nil
}
