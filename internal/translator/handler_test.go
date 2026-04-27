package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/robertprast/goop/internal/provider"
)

// stubProvider routes upstream requests to an httptest server. Rewrite
// rewrites pr.Out's URL to point at the fake host; Transport returns the
// default transport so the request actually goes over the loopback.
type stubProvider struct {
	upstream *url.URL
}

func (s *stubProvider) Name() string                 { return "stub" }
func (s *stubProvider) Prefix() string               { return "/bedrock" }
func (s *stubProvider) Transport() http.RoundTripper { return http.DefaultTransport }

func (s *stubProvider) Rewrite(pr *httputil.ProxyRequest) {
	pr.Out.URL.Scheme = s.upstream.Scheme
	pr.Out.URL.Host = s.upstream.Host
	pr.Out.Host = s.upstream.Host
	// Path is already set by the handler to /bedrock/model/<id>/converse
	// — keep it (the fake server handles whatever path arrives).
	pr.Out.Header.Del("Authorization")
}

func (s *stubProvider) ListModels(_ context.Context) ([]provider.Model, error) {
	return nil, nil
}

func TestBedrockHandler_RejectsNonPost(t *testing.T) {
	p := &stubProvider{upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}
	h := NewBedrockHandler(p, http.DefaultTransport, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/openai-bedrock/v1/chat/completions", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d want 405", rr.Code)
	}
}

func TestBedrockHandler_RejectsMissingBedrockPrefix(t *testing.T) {
	p := &stubProvider{upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}
	h := NewBedrockHandler(p, http.DefaultTransport, nil)
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai-bedrock/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", rr.Code)
	}
	var parsed map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	errObj, _ := parsed["error"].(map[string]any)
	if errObj == nil || errObj["type"] != "model_prefix" {
		t.Errorf("error.type: got %v want model_prefix; body=%s", errObj, rr.Body.String())
	}
}

func TestBedrockHandler_RejectsUnsupportedContent(t *testing.T) {
	p := &stubProvider{upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}
	h := NewBedrockHandler(p, http.DefaultTransport, nil)
	body := `{"model":"bedrock/anthropic.claude","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai-bedrock/v1/chat/completions", strings.NewReader(body))

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "unsupported_content") {
		t.Errorf("expected unsupported_content error, got %s", rr.Body.String())
	}
}

func TestBedrockHandler_RejectsBadJSON(t *testing.T) {
	p := &stubProvider{upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}
	h := NewBedrockHandler(p, http.DefaultTransport, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai-bedrock/v1/chat/completions", strings.NewReader(`{not json`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", rr.Code)
	}
}

func TestBedrockHandler_UnaryHappyPath(t *testing.T) {
	canned := map[string]any{
		"output": map[string]any{
			"message": map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{"text": "hello from bedrock"},
				},
			},
		},
		"stopReason": "end_turn",
		"usage": map[string]any{
			"inputTokens":  10,
			"outputTokens": 5,
			"totalTokens":  15,
		},
	}
	cannedJSON, _ := json.Marshal(canned)

	var capturedPath string
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(cannedJSON)
	}))
	defer srv.Close()
	upstream, _ := url.Parse(srv.URL)

	p := &stubProvider{upstream: upstream}
	h := NewBedrockHandler(p, http.DefaultTransport, nil)

	openAIReq := map[string]any{
		"model": "bedrock/anthropic.claude-3-haiku",
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	}
	body, _ := json.Marshal(openAIReq)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai-bedrock/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.HasSuffix(capturedPath, "/converse") {
		t.Errorf("upstream path: got %q want ending in /converse", capturedPath)
	}
	if !strings.Contains(capturedPath, "anthropic.claude-3-haiku") {
		t.Errorf("upstream path missing model id: %q", capturedPath)
	}
	// The body should be a Bedrock Converse-shaped JSON containing our user text.
	if !strings.Contains(string(capturedBody), `"hi"`) {
		t.Errorf("upstream body missing user text: %s", capturedBody)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, rr.Body.String())
	}
	if resp["object"] != "chat.completion" {
		t.Errorf("object: got %v want chat.completion", resp["object"])
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices: %+v", choices)
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "hello from bedrock" {
		t.Errorf("content: got %v want %q", msg["content"], "hello from bedrock")
	}
	if model, _ := resp["model"].(string); !strings.HasPrefix(model, "bedrock/") {
		t.Errorf("model field: got %v want prefix bedrock/", model)
	}
}

func TestBedrockHandler_UpstreamErrorPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-ErrorType", "AccessDeniedException")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"denied"}`))
	}))
	defer srv.Close()
	upstream, _ := url.Parse(srv.URL)

	p := &stubProvider{upstream: upstream}
	h := NewBedrockHandler(p, http.DefaultTransport, nil)

	body := `{"model":"bedrock/anthropic.claude","messages":[{"role":"user","content":"hi"}]}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai-bedrock/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d want 403", rr.Code)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != `{"message":"denied"}` {
		t.Errorf("body passthrough: got %q want %q", got, `{"message":"denied"}`)
	}
	// Upstream X-Amzn-ErrorType header should be preserved verbatim.
	if h := rr.Header().Get("X-Amzn-Errortype"); h != "AccessDeniedException" {
		t.Errorf("X-Amzn-ErrorType passthrough: got %q want AccessDeniedException", h)
	}
}

func TestBedrockHandler_StreamingSetsSSEContentType(t *testing.T) {
	// We don't need to send a real eventstream payload; the handler sets
	// the response headers before reading the body, and an empty body just
	// ends the loop cleanly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		// no body
	}))
	defer srv.Close()
	upstream, _ := url.Parse(srv.URL)

	p := &stubProvider{upstream: upstream}
	h := NewBedrockHandler(p, http.DefaultTransport, nil)

	body := `{"model":"bedrock/anthropic.claude","messages":[{"role":"user","content":"hi"}],"stream":true}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai-bedrock/v1/chat/completions", strings.NewReader(body))

	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type: got %q want text/event-stream", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control: got %q want no-cache", got)
	}
}

func TestBedrockHandler_StreamSelectsConverseStreamPath(t *testing.T) {
	// Verify the suffix path differs for streaming requests (/converse-stream
	// vs /converse). We only need to capture the upstream path.
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	upstream, _ := url.Parse(srv.URL)

	p := &stubProvider{upstream: upstream}
	h := NewBedrockHandler(p, http.DefaultTransport, nil)

	body := `{"model":"bedrock/anthropic.claude","messages":[{"role":"user","content":"hi"}],"stream":true}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai-bedrock/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rr, req)

	if !strings.HasSuffix(capturedPath, "/converse-stream") {
		t.Errorf("streaming upstream path: got %q want suffix /converse-stream", capturedPath)
	}
}
