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

// addBetaFlag inserts flag into the anthropic-beta header, preserving any
// values the client already sent. The header is a comma-joined feature list;
// Anthropic accepts it as either a single header line with commas or as
// multiple header lines. We normalize to one line for readability.
func addBetaFlag(h http.Header, flag string) {
	const key = "anthropic-beta"
	existing := h.Values(key)
	seen := map[string]bool{}
	var ordered []string
	for _, v := range existing {
		for _, part := range strings.Split(v, ",") {
			p := strings.TrimSpace(part)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			ordered = append(ordered, p)
		}
	}
	if !seen[flag] {
		ordered = append(ordered, flag)
	}
	h.Set(key, strings.Join(ordered, ","))
}

// AnthropicNativeConfig configures the native Anthropic Messages API
// passthrough at /anthropic. Clients' headers pass through verbatim;
// goop only swaps the auth credential (and adds the OAuth beta flag).
type AnthropicNativeConfig struct {
	APIKey  string                // static API key mode
	OAuth   *AnthropicOAuthSource // OAuth mode (Claude Code SSO); takes precedence
	BaseURL string                // optional override; default https://api.anthropic.com
	// FallbackVersion is used as a last resort by ListModels (goop's own outbound
	// catalog call) when the client never sends anthropic-version. It is NOT
	// injected on proxied requests — those pass through whatever the client sent.
	FallbackVersion string
}

// NewAnthropicNative builds the /anthropic passthrough.
func NewAnthropicNative(cfg AnthropicNativeConfig) (Provider, error) {
	if cfg.APIKey == "" && cfg.OAuth == nil {
		return nil, fmt.Errorf("anthropic: api_key or oauth source is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("anthropic: invalid base_url: %w", err)
	}
	fallbackVersion := cfg.FallbackVersion
	if fallbackVersion == "" {
		fallbackVersion = "2023-06-01"
	}
	return &anthropicNative{
		apiKey:          cfg.APIKey,
		oauth:           cfg.OAuth,
		fallbackVersion: fallbackVersion,
		base:            u,
		client:          &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type anthropicNative struct {
	apiKey          string
	oauth           *AnthropicOAuthSource
	fallbackVersion string // only used by ListModels
	base            *url.URL
	client          *http.Client
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

	if p.oauth != nil {
		tok, err := p.oauth.Token(pr.Out.Context())
		if err != nil {
			// We can't fail Rewrite (ReverseProxy doesn't expose an error
			// path). Forward without auth and let upstream return 401; the
			// OAuth source already logged the error.
			return
		}
		pr.Out.Header.Set("Authorization", "Bearer "+tok)
		// anthropic-beta is the only non-auth header goop has to touch: the
		// OAuth scope requires "oauth-2025-04-20" be in the list. Merge into
		// whatever Claude Code already sent (context-management-*, etc.) so
		// none of the client's feature flags are dropped.
		addBetaFlag(pr.Out.Header, "oauth-2025-04-20")
	} else {
		pr.Out.Header.Set("x-api-key", p.apiKey)
	}
	// Everything else (anthropic-version, user-agent, x-stainless-*, etc.)
	// flows through unchanged. The upstream sees the client's intent.
}

func (p *anthropicNative) ListModels(ctx context.Context) ([]Model, error) {
	endpoint := *p.base
	endpoint.Path = singleSlash(p.base.Path + "/v1/models")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	if p.oauth != nil {
		tok, err := p.oauth.Token(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	} else {
		req.Header.Set("x-api-key", p.apiKey)
	}
	// Outbound goop call (not a proxy passthrough), so we set our own version.
	req.Header.Set("anthropic-version", p.fallbackVersion)

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
