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

// GeminiNativeConfig configures the native Gemini AI Studio passthrough at
// /gemini. Clients use google-genai (or any compatible SDK); goop injects
// X-Goog-Api-Key.
type GeminiNativeConfig struct {
	APIKey  string
	BaseURL string // optional override; default https://generativelanguage.googleapis.com
}

// NewGeminiNative builds the /gemini passthrough.
func NewGeminiNative(cfg GeminiNativeConfig) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("gemini: api_key is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("gemini: invalid base_url: %w", err)
	}
	return &geminiNative{
		apiKey: cfg.APIKey,
		base:   u,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type geminiNative struct {
	apiKey string
	base   *url.URL
	client *http.Client
}

func (p *geminiNative) Name() string                 { return "gemini" }
func (p *geminiNative) Prefix() string               { return "/gemini" }
func (p *geminiNative) Transport() http.RoundTripper { return nil }

func (p *geminiNative) Rewrite(pr *httputil.ProxyRequest) {
	stripClientAuth(pr.Out.Header)

	pr.SetURL(p.base)
	pr.SetXForwarded()

	rest := strings.TrimPrefix(pr.In.URL.Path, "/gemini")
	pr.Out.URL.Path = singleSlash(p.base.Path + rest)
	pr.Out.URL.RawPath = ""

	pr.Out.Header.Set("X-Goog-Api-Key", p.apiKey)
}

func (p *geminiNative) ListModels(ctx context.Context) ([]Model, error) {
	endpoint := *p.base
	endpoint.Path = singleSlash(p.base.Path + "/v1beta/models")
	q := endpoint.Query()
	q.Set("pageSize", "1000")
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Goog-Api-Key", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: list models: HTTP %d", resp.StatusCode)
	}

	var body struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(body.Models))
	now := time.Now().Unix()
	for _, m := range body.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		out = append(out, Model{
			ID:       "gemini/" + id,
			Object:   "model",
			Created:  now,
			OwnedBy:  "google",
			Provider: "gemini",
		})
	}
	return out, nil
}
