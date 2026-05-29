package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestStripClientAuthRemovesAllUpstreamAuthHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer client-token")
	h.Set("Proxy-Authorization", "Basic abc")
	h.Set("Api-Key", "ak")
	h.Set("X-Api-Key", "xak")
	h.Set("X-Goog-Api-Key", "goog")
	// Non-auth headers must NOT be stripped — they pass through to upstream.
	h.Set("Anthropic-Version", "2023-06-01")
	h.Set("anthropic-beta", "context-management-2025-06-27")

	stripClientAuth(h)

	for _, k := range []string{
		"Authorization", "Proxy-Authorization",
		"Api-Key", "X-Api-Key", "X-Goog-Api-Key",
	} {
		if v := h.Get(k); v != "" {
			t.Errorf("expected auth header %q to be removed; got %q", k, v)
		}
	}
	// And the non-auth headers must still be present.
	for k, want := range map[string]string{
		"Anthropic-Version": "2023-06-01",
		"anthropic-beta":    "context-management-2025-06-27",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("non-auth header %q: got %q, want %q", k, got, want)
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
	h.Set("api-key", "ak")
	h.Set("PROXY-AUTHORIZATION", "Basic abc")

	stripClientAuth(h)

	probes := []string{
		"AUTHORIZATION", "authorization", "Authorization",
		"x-api-key", "X-Api-Key",
		"X-GOOG-API-KEY", "x-goog-api-key",
		"API-KEY", "Api-Key",
		"proxy-authorization", "Proxy-Authorization",
	}
	for _, k := range probes {
		if v := h.Get(k); v != "" {
			t.Errorf("expected %q lookup to be empty; got %q", k, v)
		}
	}
}

func TestAddBetaFlagPreservesExistingFlags(t *testing.T) {
	// Claude Code sends a comma-joined list of feature flags. Goop must merge
	// its OAuth flag into that list, not overwrite — otherwise features like
	// context-management are dropped and Anthropic returns 400.
	h := http.Header{}
	h.Set("anthropic-beta", "context-management-2025-06-27,fine-grained-tool-streaming-2025-05-14")

	addBetaFlag(h, "oauth-2025-04-20")

	got := h.Get("anthropic-beta")
	for _, want := range []string{
		"context-management-2025-06-27",
		"fine-grained-tool-streaming-2025-05-14",
		"oauth-2025-04-20",
	} {
		if !containsCSV(got, want) {
			t.Errorf("anthropic-beta=%q missing %q", got, want)
		}
	}
}

func TestAddBetaFlagDeduplicates(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-beta", "oauth-2025-04-20,other-flag")
	addBetaFlag(h, "oauth-2025-04-20")

	got := h.Values("anthropic-beta")
	if len(got) != 1 {
		t.Fatalf("expected single header line, got %d: %v", len(got), got)
	}
	// "oauth-2025-04-20" must appear exactly once.
	count := 0
	for _, part := range splitCSV(got[0]) {
		if part == "oauth-2025-04-20" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("oauth-2025-04-20 appears %d times in %q, want 1", count, got[0])
	}
}

func TestAddBetaFlagAddsWhenAbsent(t *testing.T) {
	h := http.Header{}
	addBetaFlag(h, "oauth-2025-04-20")
	if got := h.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Errorf("got %q, want oauth-2025-04-20", got)
	}
}

func containsCSV(csv, want string) bool {
	for _, p := range splitCSV(csv) {
		if p == want {
			return true
		}
	}
	return false
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
