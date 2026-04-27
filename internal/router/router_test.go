package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"

	"github.com/robertprast/goop/internal/provider"
)

// stubProvider is a minimal provider.Provider for routing tests. It records
// the last request so we can assert path rewrites + body mutations landed.
type stubProvider struct {
	name, prefix string
	last         *http.Request
	lastBody     []byte
}

func (s *stubProvider) Name() string                                  { return s.name }
func (s *stubProvider) Prefix() string                                { return s.prefix }
func (s *stubProvider) Rewrite(_ *httputil.ProxyRequest)              {}
func (s *stubProvider) Transport() http.RoundTripper                  { return nil }
func (s *stubProvider) ListModels(_ context.Context) ([]provider.Model, error) {
	return nil, nil
}

func TestResolve(t *testing.T) {
	together := &stubProvider{name: "together", prefix: "/together"}
	openai := &stubProvider{name: "openai", prefix: "/openai"}
	bedrockOpenAI := &stubProvider{name: "bedrock-openai", prefix: "/bedrock-openai"}
	geminiOpenAI := &stubProvider{name: "gemini-openai", prefix: "/gemini-openai"}
	reg := provider.NewRegistry(together, openai, bedrockOpenAI, geminiOpenAI)

	translatorHit := false
	bedrockTrans := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		translatorHit = true
		w.WriteHeader(http.StatusOK)
	})
	r := NewResolver(reg, bedrockTrans, nil)

	tests := []struct {
		name        string
		modelID     string
		wantPrefix  string // empty if expecting translator
		wantTrans   bool
		wantStrip   string
		wantErr     string
	}{
		{
			name:       "together passthrough",
			modelID:    "together/moonshotai/Kimi-K2.6",
			wantPrefix: "/together",
			wantStrip:  "moonshotai/Kimi-K2.6",
		},
		{
			name:       "openai passthrough",
			modelID:    "openai/gpt-4o",
			wantPrefix: "/openai",
			wantStrip:  "gpt-4o",
		},
		{
			// LiteLLM-style namespaced ID (gemini provider, openai-compat path).
			name:       "gemini-openai uses provider name as namespace",
			modelID:    "gemini-openai/gemini-2.0-flash",
			wantPrefix: "/gemini-openai",
			wantStrip:  "gemini-2.0-flash",
		},
		{
			name:       "bedrock mantle (gpt-oss)",
			modelID:    "bedrock/openai.gpt-oss-120b-1:0",
			wantPrefix: "/bedrock-openai",
			wantStrip:  "openai.gpt-oss-120b-1:0",
		},
		{
			name:      "bedrock anthropic via converse translator",
			modelID:   "bedrock/us.anthropic.claude-sonnet-4-5-20250929-v1:0",
			wantTrans: true,
			// Translator strips the prefix itself, so router leaves the
			// model ID intact.
			wantStrip: "bedrock/us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		},
		{
			name:      "bedrock nova (non-anthropic, non-mantle) → translator",
			modelID:   "bedrock/us.amazon.nova-lite-v1:0",
			wantTrans: true,
			wantStrip: "bedrock/us.amazon.nova-lite-v1:0",
		},
		{
			name:    "empty model rejected",
			modelID: "",
			wantErr: "missing required field",
		},
		{
			name:    "no namespace prefix rejected",
			modelID: "gpt-4o",
			wantErr: "no goop namespace prefix",
		},
		{
			name:    "unknown provider rejected",
			modelID: "fictional/some-model",
			wantErr: "unknown provider",
		},
	}

	// "bedrock/" with no rest still routes to the translator when one is
	// configured; the translator emits its own 400. We don't second-guess
	// that here.

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := r.Resolve(tc.modelID)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got success: %+v", tc.wantErr, d)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantTrans {
				if d.TranslatorHandler == nil {
					t.Fatalf("expected TranslatorHandler, got passthrough %q", d.PassthroughPrefix)
				}
			} else {
				if d.TranslatorHandler != nil {
					t.Fatalf("expected passthrough, got translator")
				}
				if d.PassthroughPrefix != tc.wantPrefix {
					t.Fatalf("passthrough prefix: want %q, got %q", tc.wantPrefix, d.PassthroughPrefix)
				}
			}
			if d.StrippedModel != tc.wantStrip {
				t.Fatalf("stripped model: want %q, got %q", tc.wantStrip, d.StrippedModel)
			}
		})
	}

	// One end-to-end check that the handler rewrites path + body and hands
	// off to the inner proxy. Fakes the proxy with a handler that records
	// the request it sees.
	t.Run("handler rewrites path and strips model prefix", func(t *testing.T) {
		var seen *http.Request
		var seenBody []byte
		inner := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = req
			seenBody, _ = io.ReadAll(req.Body)
			w.WriteHeader(http.StatusOK)
		})

		body := []byte(`{"model":"together/moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"hi"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		r.Handler(inner).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
		}
		if seen == nil {
			t.Fatal("inner proxy was not invoked")
		}
		if got := seen.URL.Path; got != "/together/v1/chat/completions" {
			t.Fatalf("path: want /together/v1/chat/completions, got %q", got)
		}
		var forwarded map[string]any
		if err := json.Unmarshal(seenBody, &forwarded); err != nil {
			t.Fatalf("forwarded body parse: %v body=%s", err, seenBody)
		}
		if got := forwarded["model"]; got != "moonshotai/Kimi-K2.6" {
			t.Fatalf("model: want moonshotai/Kimi-K2.6, got %v", got)
		}
		if seen.ContentLength != int64(len(seenBody)) {
			t.Fatalf("content-length mismatch: header %d body %d", seen.ContentLength, len(seenBody))
		}
	})

	t.Run("handler dispatches bedrock anthropic to translator", func(t *testing.T) {
		translatorHit = false
		body := []byte(`{"model":"bedrock/us.anthropic.claude-sonnet-4-5-20250929-v1:0","messages":[]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r.Handler(http.NotFoundHandler()).ServeHTTP(w, req)
		if !translatorHit {
			t.Fatalf("expected translator to be invoked, status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("missing model returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"messages":[]}`)))
		w := httptest.NewRecorder()
		r.Handler(http.NotFoundHandler()).ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status: want 400, got %d", w.Code)
		}
	})

	t.Run("non-POST rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
		w := httptest.NewRecorder()
		r.Handler(http.NotFoundHandler()).ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status: want 405, got %d", w.Code)
		}
	})
}
