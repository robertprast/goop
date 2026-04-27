package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareDisabledWhenKeyEmpty(t *testing.T) {
	mw := Middleware("")
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !called {
		t.Fatal("handler not called")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status: %d", rec.Code)
	}
}

func TestMiddlewareRejectsMissing(t *testing.T) {
	mw := Middleware("supersecret")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not be called")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: %d", rec.Code)
	}
}

func TestMiddlewareAcceptsCorrect(t *testing.T) {
	mw := Middleware("supersecret")
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer supersecret")
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatal("handler not called")
	}
}

func TestMiddlewareRejectsWrong(t *testing.T) {
	mw := Middleware("supersecret")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not be called")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wrongkey1")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: %d", rec.Code)
	}
}

// A trailing space after the token must NOT match. Authorization is an
// exact-byte comparison; whitespace is part of the token surface.
func TestMiddlewareRejectsTrailingSpace(t *testing.T) {
	mw := Middleware("supersecret")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not be called")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer supersecret ")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for trailing-space token; got %d", rec.Code)
	}
}
