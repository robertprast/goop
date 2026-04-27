package translator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/robertprast/goop/internal/provider"
)

type fakeListProvider struct {
	stubProvider
	models []provider.Model
	err    error
	calls  int
}

func (f *fakeListProvider) ListModels(_ context.Context) ([]provider.Model, error) {
	f.calls++
	return f.models, f.err
}

func TestModelsHandler_HappyPath(t *testing.T) {
	want := []provider.Model{
		{ID: "bedrock/anthropic.claude-sonnet-4-5-20250929-v1:0", Object: "model", OwnedBy: "Anthropic", Provider: "bedrock"},
		{ID: "bedrock/us.amazon.nova-pro-v1:0", Object: "model", OwnedBy: "Amazon", Provider: "bedrock"},
	}
	p := &fakeListProvider{models: want}
	h := NewModelsHandler(p, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bedrock-translate/v1/models", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}

	var body struct {
		Object string           `json:"object"`
		Data   []provider.Model `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Object != "list" {
		t.Errorf("object = %q, want list", body.Object)
	}
	if len(body.Data) != len(want) {
		t.Fatalf("data len = %d, want %d", len(body.Data), len(want))
	}
	for i, m := range body.Data {
		if m.ID != want[i].ID {
			t.Errorf("data[%d].id = %q, want %q", i, m.ID, want[i].ID)
		}
	}
	if p.calls != 1 {
		t.Errorf("ListModels called %d times, want 1", p.calls)
	}
}

func TestModelsHandler_EmptyListIsValidJSON(t *testing.T) {
	// Provider that returns (nil, nil) shouldn't yield "data": null — that
	// breaks strict OpenAI clients that expect an array.
	p := &fakeListProvider{models: nil}
	h := NewModelsHandler(p, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bedrock-translate/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data []provider.Model `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Data == nil {
		t.Fatalf("data is null; want []")
	}
	if len(body.Data) != 0 {
		t.Fatalf("data = %v, want []", body.Data)
	}
}

func TestModelsHandler_RejectsPost(t *testing.T) {
	h := NewModelsHandler(&fakeListProvider{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/bedrock-translate/v1/models", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestModelsHandler_HEADOmitsBody(t *testing.T) {
	p := &fakeListProvider{models: []provider.Model{{ID: "bedrock/x"}}}
	h := NewModelsHandler(p, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/bedrock-translate/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body should be empty, got %d bytes", rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q, want application/json", got)
	}
}

func TestModelsHandler_UpstreamErrorIs502(t *testing.T) {
	p := &fakeListProvider{err: errors.New("bedrock unavailable")}
	h := NewModelsHandler(p, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bedrock-translate/v1/models", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal error envelope: %v", err)
	}
	if env.Error.Type != "list_models" {
		t.Errorf("error.type = %q, want list_models", env.Error.Type)
	}
}
