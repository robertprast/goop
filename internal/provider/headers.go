package provider

import "net/http"

// stripClientAuth removes only the headers a client could use to spoof
// upstream authentication. Everything else (anthropic-version, anthropic-beta,
// user-agent, etc.) is passed through unchanged so the upstream sees exactly
// what the client intended — goop only swaps the credentials.
func stripClientAuth(h http.Header) {
	for _, k := range []string{
		"Authorization", "Proxy-Authorization",
		"Api-Key", "X-Api-Key",
		"X-Goog-Api-Key",
	} {
		h.Del(k)
	}
}
