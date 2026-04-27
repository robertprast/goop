package provider

import "net/http"

// stripClientAuth removes every header a client could use to inject upstream
// authentication into the proxied request. After this, the provider's
// Rewrite is responsible for setting the correct upstream auth.
func stripClientAuth(h http.Header) {
	for _, k := range []string{
		"Authorization", "Proxy-Authorization",
		"Api-Key", "X-Api-Key",
		"X-Goog-Api-Key",
		"Anthropic-Version",
	} {
		h.Del(k)
	}
}
