package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestJoinUpstreamPath_Table(t *testing.T) {
	cases := []struct {
		name       string
		base       string
		v1Override string
		rest       string
		want       string
	}{
		{"plain v1 models", "", "", "/v1/models", "/v1/models"},
		{"plain v1 chat", "", "", "/v1/chat/completions", "/v1/chat/completions"},
		{"mantle base prefix", "/openai", "", "/v1/models", "/openai/v1/models"},
		{"gemini override", "", "/v1beta/openai", "/v1/models", "/v1beta/openai/models"},
		{"gemini override exact /v1", "", "/v1beta/openai", "/v1", "/v1beta/openai"},
		// When rest has no /v1 prefix the override is NOT applied — rest is
		// appended verbatim to basePath. This reflects current code behavior;
		// the override only triggers on explicit "/v1" matches.
		{"override no /v1 prefix", "", "/v1beta/openai", "/v2/models", "/v2/models"},
		{"empty rest singleSlash", "/foo", "", "", "/foo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := joinUpstreamPath(c.base, c.v1Override, c.rest)
			if got != c.want {
				t.Errorf("joinUpstreamPath(%q,%q,%q): got %q want %q", c.base, c.v1Override, c.rest, got, c.want)
			}
		})
	}
}

func TestSingleSlash(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/a//b", "/a/b"},
		{"a/b", "/a/b"},
		{"", "/"},
		{"/", "/"},
		{"/a///b//c", "/a/b/c"},
		{"a", "/a"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := singleSlash(c.in)
			if got != c.want {
				t.Errorf("singleSlash(%q): got %q want %q", c.in, got, c.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "fallback"); got != "fallback" {
		t.Errorf("got %q want fallback", got)
	}
	if got := firstNonEmpty("primary", "fallback"); got != "primary" {
		t.Errorf("got %q want primary", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("got %q want empty", got)
	}
}

func TestNewOpenAICompat_RequiresFields(t *testing.T) {
	if _, err := NewOpenAICompat(OpenAICompatConfig{}); err == nil {
		t.Error("want error with empty config")
	}
	u, _ := url.Parse("https://example.com")
	if _, err := NewOpenAICompat(OpenAICompatConfig{Name: "n", BaseURL: u}); err == nil {
		t.Error("want error with empty prefix")
	}
	if _, err := NewOpenAICompat(OpenAICompatConfig{Name: "n", Prefix: "/n"}); err == nil {
		t.Error("want error with nil base url")
	}
}

func TestOpenAICompat_ListModels_BareArrayLikeTogether(t *testing.T) {
	// Together AI returns a bare JSON array, not the OpenAI {"data":[...]} wrapper.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.Error(w, "wrong path", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":"x","object":"model"},{"id":"y"}]`))
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p, err := NewOpenAICompat(OpenAICompatConfig{
		Name:          "together",
		Prefix:        "/together",
		BaseURL:       u,
		ModelsPath:    "/v1/models",
		ModelIDPrefix: "together/",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompat: %v", err)
	}
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len: got %d want 2 (%+v)", len(models), models)
	}
	if models[0].ID != "together/x" {
		t.Errorf("ID[0]: got %q want together/x", models[0].ID)
	}
	if models[0].Object != "model" {
		t.Errorf("Object[0]: got %q want model", models[0].Object)
	}
	// Default object fallback when missing in upstream.
	if models[1].Object != "model" {
		t.Errorf("Object[1]: got %q want fallback model", models[1].Object)
	}
	if models[1].Provider != "together" {
		t.Errorf("Provider: got %q want together", models[1].Provider)
	}
}

func TestOpenAICompat_ListModels_WrappedDataShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"models/gemini-pro","object":"model"}]}`))
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p, err := NewOpenAICompat(OpenAICompatConfig{
		Name:          "gemini-openai",
		Prefix:        "/gemini-openai",
		BaseURL:       u,
		ModelsPath:    "/v1/models",
		ModelIDPrefix: "gemini/",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompat: %v", err)
	}
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len: %d", len(models))
	}
	// "models/" prefix should be trimmed before model_id_prefix is applied.
	if models[0].ID != "gemini/gemini-pro" {
		t.Errorf("ID: got %q want gemini/gemini-pro", models[0].ID)
	}
}

func TestOpenAICompat_ListModels_Empty_ModelsPath(t *testing.T) {
	u, _ := url.Parse("https://example.com")
	p, err := NewOpenAICompat(OpenAICompatConfig{
		Name:    "x",
		Prefix:  "/x",
		BaseURL: u,
		// ModelsPath empty disables listing
	})
	if err != nil {
		t.Fatalf("NewOpenAICompat: %v", err)
	}
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Errorf("ListModels: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for empty ModelsPath", got)
	}
}

func TestOpenAICompat_ListModels_HTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", 403)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	p, err := NewOpenAICompat(OpenAICompatConfig{
		Name: "x", Prefix: "/x", BaseURL: u, ModelsPath: "/v1/models",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompat: %v", err)
	}
	if _, err := p.ListModels(context.Background()); err == nil {
		t.Fatal("want error from non-200 upstream, got nil")
	}
}

func TestOpenAICompat_NameAndPrefix(t *testing.T) {
	u, _ := url.Parse("https://example.com")
	p, err := NewOpenAICompat(OpenAICompatConfig{Name: "alpha", Prefix: "/alpha", BaseURL: u})
	if err != nil {
		t.Fatalf("NewOpenAICompat: %v", err)
	}
	if got := p.Name(); got != "alpha" {
		t.Errorf("Name: got %q want alpha", got)
	}
	if got := p.Prefix(); got != "/alpha" {
		t.Errorf("Prefix: got %q want /alpha", got)
	}
}
