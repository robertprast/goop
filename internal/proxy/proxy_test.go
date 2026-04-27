package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/robertprast/goop/internal/provider"
)

func TestProviderPrefixRouting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Got-Path", r.URL.Path)
		w.Header().Set("X-Got-Auth", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	upURL, _ := url.Parse(upstream.URL)

	p, err := provider.NewOpenAICompat(provider.OpenAICompatConfig{
		Name:            "fake",
		Prefix:          "/fake",
		BaseURL:         upURL,
		AuthHeader:      "Authorization",
		AuthValueFormat: "Bearer %s",
		AuthValue:       "secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := New(provider.NewRegistry(p), nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/fake/v1/anything?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Got-Path"); got != "/v1/anything" {
		t.Errorf("upstream path: want /v1/anything, got %q", got)
	}
	if got := resp.Header.Get("X-Got-Auth"); got != "Bearer secret" {
		t.Errorf("upstream auth: want %q, got %q", "Bearer secret", got)
	}
}

func TestUnknownPrefix404(t *testing.T) {
	srv := New(provider.NewRegistry(), nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/nowhere")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	srv := New(provider.NewRegistry(), nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"ok"`) {
		t.Errorf("body: %s", body)
	}
}

func TestReadyEndpoint(t *testing.T) {
	// Empty registry → not ready.
	emptySrv := New(provider.NewRegistry(), nil, nil)
	ets := httptest.NewServer(emptySrv.Handler())
	defer ets.Close()
	resp, err := http.Get(ets.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("empty registry: want 503, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"not_ready"`) {
		t.Errorf("empty registry body: %s", body)
	}

	// At least one provider → ready.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	upURL, _ := url.Parse(upstream.URL)
	p, err := provider.NewOpenAICompat(provider.OpenAICompatConfig{
		Name: "fake", Prefix: "/fake", BaseURL: upURL,
		AuthHeader: "Authorization", AuthValueFormat: "Bearer %s", AuthValue: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(provider.NewRegistry(p), nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp2, err := http.Get(ts.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("with provider: want 200, got %d", resp2.StatusCode)
	}
	body2, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body2), `"ok"`) {
		t.Errorf("with provider body: %s", body2)
	}
}

func TestMaxBodyBytesPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain whatever made it through so the test can observe the cap.
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	upURL, _ := url.Parse(upstream.URL)
	p, err := provider.NewOpenAICompat(provider.OpenAICompatConfig{
		Name: "fake", Prefix: "/fake", BaseURL: upURL,
		AuthHeader: "Authorization", AuthValueFormat: "Bearer %s", AuthValue: "x",
	})
	if err != nil {
		t.Fatal(err)
	}

	const limit = 1024
	srv := NewWithLimit(provider.NewRegistry(p), nil, nil, limit)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Body comfortably exceeds the limit.
	big := bytes.Repeat([]byte("A"), limit*4)
	resp, err := http.Post(ts.URL+"/fake/v1/anything", "application/octet-stream", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized POST: want 413, got %d", resp.StatusCode)
	}

	// Body within the limit still goes through.
	small := bytes.Repeat([]byte("a"), 16)
	resp2, err := http.Post(ts.URL+"/fake/v1/anything", "application/octet-stream", bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("small POST: want 200, got %d", resp2.StatusCode)
	}
}

func TestPrefixBoundary(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	upURL, _ := url.Parse(upstream.URL)
	p, _ := provider.NewOpenAICompat(provider.OpenAICompatConfig{
		Name: "bedrock", Prefix: "/bedrock", BaseURL: upURL,
		AuthHeader: "Authorization", AuthValueFormat: "Bearer %s", AuthValue: "x",
	})
	srv := New(provider.NewRegistry(p), nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// /bedrockfoo should NOT match /bedrock prefix.
	resp, err := http.Get(ts.URL + "/bedrockfoo/v1/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for non-boundary prefix, got %d", resp.StatusCode)
	}
}
