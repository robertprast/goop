package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// OpenAICompatConfig parameterises an OpenAI-wire-compatible upstream.
//
// One implementation handles every provider that exposes an OpenAI-compat API:
// OpenAI itself, Azure (v1), Together, Anthropic's compat layer, Gemini's
// /v1beta/openai, Vertex's /endpoints/openapi, and Bedrock's Mantle endpoint.
// What changes is the base URL, the auth header scheme, and the path prefix
// served by goop — so we keep one struct and one set of behaviors.
type OpenAICompatConfig struct {
	Name    string   // logical name, e.g. "openai", "together", "bedrock-openai"
	Prefix  string   // URL prefix served by goop, e.g. "/openai"
	BaseURL *url.URL // upstream base, e.g. https://api.openai.com (no trailing /v1)

	// AuthHeader and AuthValueFormat control how upstream auth is injected.
	// Examples:
	//   AuthHeader: "Authorization", AuthValueFormat: "Bearer %s"
	//   AuthHeader: "x-api-key",     AuthValueFormat: "%s"
	//   AuthHeader: "api-key",       AuthValueFormat: "%s"  // Azure
	AuthHeader      string
	AuthValueFormat string
	AuthValue       string // the secret itself; empty disables auth injection

	// ExtraHeaders are added verbatim (e.g. anthropic-version: 2023-06-01).
	ExtraHeaders map[string]string

	// QueryParams are merged into the outgoing query (e.g. api-version for Azure classic).
	QueryParams map[string]string

	// ModelsPath is the upstream path that returns OpenAI-shaped {data: [...]}
	// (e.g. "/v1/models"). Empty disables ListModels.
	ModelsPath string

	// ModelIDPrefix is prepended to model IDs from this provider (e.g. "openai/").
	ModelIDPrefix string

	// V1PathOverride, when non-empty, substitutes the /v1 prefix in the
	// client's path with this value. Use it for providers whose OpenAI-compat
	// endpoint is mounted under a different path (e.g. Gemini's /v1beta/openai
	// or Vertex's /endpoints/openapi). When empty the default semantics apply:
	// the client's full path (including /v1) is appended to BaseURL.Path.
	V1PathOverride string

	// Transport, when set, replaces the default HTTP transport for both proxy
	// forwarding and ListModels. Used by Bedrock-Mantle for SigV4 signing.
	HTTPTransport http.RoundTripper

	// HTTPClient is used for ListModels (defaults to a 15s-timeout client).
	HTTPClient *http.Client
}

// NewOpenAICompat constructs a provider from the parameterised config.
func NewOpenAICompat(cfg OpenAICompatConfig) (Provider, error) {
	if cfg.Name == "" || cfg.Prefix == "" || cfg.BaseURL == nil {
		return nil, fmt.Errorf("openai-compat: name, prefix, and base_url are required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Transport: cfg.HTTPTransport, // nil → http.DefaultTransport inside http.Client
			Timeout:   15 * time.Second,
		}
	}
	return &openAICompat{cfg: cfg}, nil
}

type openAICompat struct{ cfg OpenAICompatConfig }

func (p *openAICompat) Name() string                 { return p.cfg.Name }
func (p *openAICompat) Prefix() string               { return p.cfg.Prefix }
func (p *openAICompat) Transport() http.RoundTripper { return p.cfg.HTTPTransport }

func (p *openAICompat) Rewrite(pr *httputil.ProxyRequest) {
	stripClientAuth(pr.Out.Header)

	pr.SetURL(p.cfg.BaseURL)
	pr.SetXForwarded()

	// Strip our prefix; whatever remains is forwarded under the BaseURL path.
	in := pr.In.URL.Path
	rest := strings.TrimPrefix(in, p.cfg.Prefix)
	pr.Out.URL.Path = joinUpstreamPath(p.cfg.BaseURL.Path, p.cfg.V1PathOverride,rest)
	pr.Out.URL.RawPath = "" // let net/url re-encode

	// Replace with upstream auth.
	if p.cfg.AuthHeader != "" && p.cfg.AuthValue != "" {
		fmtStr := p.cfg.AuthValueFormat
		if fmtStr == "" {
			fmtStr = "%s"
		}
		pr.Out.Header.Set(p.cfg.AuthHeader, fmt.Sprintf(fmtStr, p.cfg.AuthValue))
	}
	for k, v := range p.cfg.ExtraHeaders {
		pr.Out.Header.Set(k, v)
	}
	if len(p.cfg.QueryParams) > 0 {
		q := pr.Out.URL.Query()
		for k, v := range p.cfg.QueryParams {
			q.Set(k, v)
		}
		pr.Out.URL.RawQuery = q.Encode()
	}
}

func (p *openAICompat) ListModels(ctx context.Context) ([]Model, error) {
	if p.cfg.ModelsPath == "" {
		return nil, nil
	}
	endpoint := *p.cfg.BaseURL
	endpoint.Path = joinUpstreamPath(p.cfg.BaseURL.Path, p.cfg.V1PathOverride,p.cfg.ModelsPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	if p.cfg.AuthHeader != "" && p.cfg.AuthValue != "" {
		fmtStr := p.cfg.AuthValueFormat
		if fmtStr == "" {
			fmtStr = "%s"
		}
		req.Header.Set(p.cfg.AuthHeader, fmt.Sprintf(fmtStr, p.cfg.AuthValue))
	}
	for k, v := range p.cfg.ExtraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: list models: HTTP %d", p.cfg.Name, resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	// Most providers wrap the list in {"data": [...]} (OpenAI native, Gemini,
	// Anthropic). Together AI returns a bare JSON array. Try the wrapper
	// first, then fall back to the array shape.
	var entries []modelEntry
	var wrapped struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Data != nil {
		entries = wrapped.Data
	} else if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s: parse models: %w", p.cfg.Name, err)
	}
	out := make([]Model, 0, len(entries))
	for _, m := range entries {
		id := strings.TrimPrefix(m.ID, "models/")
		out = append(out, Model{
			ID:       p.cfg.ModelIDPrefix + id,
			Object:   firstNonEmpty(m.Object, "model"),
			Created:  m.Created,
			OwnedBy:  m.OwnedBy,
			Provider: p.cfg.Name,
		})
	}
	return out, nil
}

// joinUpstreamPath joins basePath + rest, with optional /v1 substitution.
// When v1Override is non-empty and rest begins with /v1, /v1 is replaced
// with v1Override; otherwise rest is appended verbatim to basePath.
func joinUpstreamPath(basePath, v1Override, rest string) string {
	if v1Override != "" {
		switch {
		case strings.HasPrefix(rest, "/v1/"):
			return singleSlash(basePath + v1Override + strings.TrimPrefix(rest, "/v1"))
		case rest == "/v1":
			return singleSlash(basePath + v1Override)
		}
	}
	return singleSlash(basePath + rest)
}

func singleSlash(s string) string {
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	if s == "" {
		return "/"
	}
	if s[0] != '/' {
		s = "/" + s
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
