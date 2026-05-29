package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// ChatGPTNativeConfig configures the /chatgpt provider — a passthrough to the
// ChatGPT backend used by the Codex CLI under `auth_mode: chatgpt`. There is
// no static-key mode: this endpoint *requires* a user OAuth session, so
// OpenAIOAuthSource is mandatory.
type ChatGPTNativeConfig struct {
	OAuth   *OpenAIOAuthSource
	BaseURL string // default https://chatgpt.com/backend-api/codex
}

// NewChatGPTNative builds the /chatgpt passthrough. The Codex CLI talks to
// https://chatgpt.com/backend-api/codex/responses (OpenAI Responses API
// shape) authenticated as the signed-in ChatGPT user, with a
// chatgpt-account-id header pulled from the auth file. Goop just swaps
// auth on the fly so clients ship dummies.
func NewChatGPTNative(cfg ChatGPTNativeConfig) (Provider, error) {
	if cfg.OAuth == nil {
		return nil, fmt.Errorf("chatgpt: oauth source is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://chatgpt.com/backend-api/codex"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: invalid base_url: %w", err)
	}
	return &chatgptNative{
		oauth: cfg.OAuth,
		base:  u,
	}, nil
}

type chatgptNative struct {
	oauth *OpenAIOAuthSource
	base  *url.URL
}

func (p *chatgptNative) Name() string                 { return "chatgpt" }
func (p *chatgptNative) Prefix() string               { return "/chatgpt" }
func (p *chatgptNative) Transport() http.RoundTripper { return nil }

func (p *chatgptNative) Rewrite(pr *httputil.ProxyRequest) {
	stripClientAuth(pr.Out.Header)

	pr.SetURL(p.base)
	pr.SetXForwarded()

	rest := strings.TrimPrefix(pr.In.URL.Path, "/chatgpt")
	pr.Out.URL.Path = singleSlash(p.base.Path + rest)
	pr.Out.URL.RawPath = ""

	tok, accountID, err := p.oauth.Token(pr.Out.Context())
	if err != nil {
		// Same reasoning as anthropicNative.Rewrite: ReverseProxy doesn't
		// give us an error return. Forward unauthenticated and let upstream
		// 401; the OAuth source already logged.
		return
	}
	pr.Out.Header.Set("Authorization", "Bearer "+tok)
	pr.Out.Header.Set("chatgpt-account-id", accountID)
}

// ListModels: the ChatGPT backend doesn't expose a public model catalog over
// this endpoint (the CLI hard-codes its model list), so we return nothing
// and let goop's aggregator skip us.
func (p *chatgptNative) ListModels(ctx context.Context) ([]Model, error) {
	return nil, nil
}
