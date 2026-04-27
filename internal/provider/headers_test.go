package provider

import (
	"net/http"
	"testing"
)

func TestStripClientAuthRemovesAllUpstreamAuthHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer client-token")
	h.Set("Proxy-Authorization", "Basic abc")
	h.Set("Api-Key", "ak")
	h.Set("X-Api-Key", "xak")
	h.Set("X-Goog-Api-Key", "goog")
	h.Set("Anthropic-Version", "2023-06-01")

	stripClientAuth(h)

	for _, k := range []string{
		"Authorization", "Proxy-Authorization",
		"Api-Key", "X-Api-Key",
		"X-Goog-Api-Key", "Anthropic-Version",
	} {
		if v := h.Get(k); v != "" {
			t.Errorf("expected %q to be removed; got %q", k, v)
		}
	}
}

func TestStripClientAuthIsCaseInsensitive(t *testing.T) {
	// http.Header normalizes keys via textproto.CanonicalMIMEHeaderKey.
	// Setting headers with arbitrary case should still be removed.
	h := http.Header{}
	h.Set("authorization", "Bearer client-token")
	h.Set("X-API-KEY", "xak")
	h.Set("x-goog-api-key", "goog")
	h.Set("ANTHROPIC-VERSION", "2023-06-01")
	h.Set("api-key", "ak")
	h.Set("PROXY-AUTHORIZATION", "Basic abc")

	stripClientAuth(h)

	// Probe via different cases — all must be empty after stripping.
	probes := []string{
		"AUTHORIZATION", "authorization", "Authorization",
		"x-api-key", "X-Api-Key",
		"X-GOOG-API-KEY", "x-goog-api-key",
		"anthropic-version", "Anthropic-Version",
		"API-KEY", "Api-Key",
		"proxy-authorization", "Proxy-Authorization",
	}
	for _, k := range probes {
		if v := h.Get(k); v != "" {
			t.Errorf("expected %q lookup to be empty; got %q", k, v)
		}
	}
}

func TestStripClientAuthLeavesOtherHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "goop-test")
	h.Set("X-Request-Id", "abc-123")
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Bearer client-token") // should be removed
	h.Set("X-Custom-Header", "value")

	stripClientAuth(h)

	if got := h.Get("Authorization"); got != "" {
		t.Errorf("Authorization not stripped: %q", got)
	}
	want := map[string]string{
		"Content-Type":    "application/json",
		"User-Agent":      "goop-test",
		"X-Request-Id":    "abc-123",
		"Accept":          "application/json",
		"X-Custom-Header": "value",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("header %q: got %q, want %q", k, got, v)
		}
	}
}
