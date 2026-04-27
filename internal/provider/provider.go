// Package provider abstracts upstream LLM hosts behind a uniform interface
// so the proxy layer can route, sign, and forward requests without baking in
// per-provider knowledge. Providers come in two flavors:
//
//   - Native passthrough (e.g. /bedrock, /gemini, /anthropic): clients use the
//     provider's own SDK; goop only injects auth and forwards bytes.
//   - OpenAI-compatible passthrough (e.g. /openai, /azure, /together,
//     /gemini-openai, /bedrock-openai): clients speak OpenAI's wire format
//     and goop forwards to the provider's OpenAI-compat endpoint.
package provider

import (
	"context"
	"net/http"
	"net/http/httputil"
)

// Provider is the contract implemented by every upstream we proxy to.
type Provider interface {
	// Name is a stable identifier used in logs and the model namespace.
	Name() string

	// Prefix is the URL prefix this provider serves under (e.g. "/openai").
	// The proxy strips this before forwarding upstream.
	Prefix() string

	// Rewrite mutates the outgoing request: rewrites URL, host, and injects
	// upstream auth headers. It must NOT read pr.Out.Body — body buffering
	// belongs in Transport (see below).
	Rewrite(pr *httputil.ProxyRequest)

	// Transport returns a custom RoundTripper for this provider, or nil to
	// use the proxy's shared default transport. SigV4 providers return a
	// signing transport here so signing happens after the body is fully
	// streamed from the client.
	Transport() http.RoundTripper

	// ListModels fetches the live model catalog. Providers that don't
	// support listing return (nil, nil).
	ListModels(ctx context.Context) ([]Model, error)
}

// Model is the unified model record served by /v1/models. It is intentionally
// a strict subset of OpenAI's model object — anything else belongs in the
// per-provider native API.
type Model struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Created  int64  `json:"created,omitempty"`
	OwnedBy  string `json:"owned_by,omitempty"`
	Provider string `json:"provider"`
}

// Registry holds the configured providers, keyed by Name().
type Registry struct {
	byName   map[string]Provider
	byPrefix map[string]Provider
	order    []Provider
}

// NewRegistry builds a registry from a list of providers.
func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{
		byName:   make(map[string]Provider, len(ps)),
		byPrefix: make(map[string]Provider, len(ps)),
		order:    make([]Provider, 0, len(ps)),
	}
	for _, p := range ps {
		if p == nil {
			continue
		}
		r.byName[p.Name()] = p
		r.byPrefix[p.Prefix()] = p
		r.order = append(r.order, p)
	}
	return r
}

// ByName returns the provider with the given Name(), or nil if not found.
func (r *Registry) ByName(name string) Provider { return r.byName[name] }

// ByPrefix returns the provider for the longest matching URL prefix, or nil.
func (r *Registry) ByPrefix(path string) Provider {
	// Longest-prefix match so /bedrock-openai wins over /bedrock.
	var match Provider
	for prefix, p := range r.byPrefix {
		if hasPrefix(path, prefix) && (match == nil || len(prefix) > len(match.Prefix())) {
			match = p
		}
	}
	return match
}

// All returns providers in registration order.
func (r *Registry) All() []Provider { return r.order }

func hasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	if s[:len(prefix)] != prefix {
		return false
	}
	// Must be followed by end-of-string or '/' so /bedrock doesn't match /bedrockfoo.
	return len(s) == len(prefix) || s[len(prefix)] == '/'
}
