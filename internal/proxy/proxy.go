// Package proxy implements goop's HTTP front-end: a single
// httputil.ReverseProxy per provider that injects upstream auth and forwards
// streaming responses without buffering.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"

	"github.com/robertprast/goop/internal/models"
	"github.com/robertprast/goop/internal/provider"
)

// defaultMaxBodyBytes mirrors the translator's 10 MiB cap so the generic
// passthrough proxy can't be used to OOM the process with a streaming POST.
const defaultMaxBodyBytes int64 = 10 << 20

// Server holds the configured providers and emits an http.Handler that
// dispatches by URL prefix.
type Server struct {
	registry     *provider.Registry
	logger       *slog.Logger
	default_     *http.Transport
	models       *models.Aggregator
	maxBodyBytes int64

	proxies map[string]*httputil.ReverseProxy
}

// New builds a Server from a provider registry. agg may be nil to disable
// the unified /v1/models endpoint.
func New(reg *provider.Registry, agg *models.Aggregator, logger *slog.Logger) *Server {
	return NewWithLimit(reg, agg, logger, defaultMaxBodyBytes)
}

// NewWithLimit is like New but accepts an explicit maximum request body size
// (in bytes) to enforce on passthrough proxy requests. A value <= 0 falls
// back to the default 10 MiB cap.
func NewWithLimit(reg *provider.Registry, agg *models.Aggregator, logger *slog.Logger, maxBodyBytes int64) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	defaultT := DefaultTransport()

	proxies := make(map[string]*httputil.ReverseProxy, len(reg.All()))
	for _, p := range reg.All() {
		transport := http.RoundTripper(defaultT)
		if pt := p.Transport(); pt != nil {
			transport = pt
		}
		proxies[p.Name()] = &httputil.ReverseProxy{
			Rewrite:        p.Rewrite,
			Transport:      transport,
			FlushInterval:  -1, // immediate flush; SSE will stream as bytes arrive
			ModifyResponse: modifyResponse(logger, p.Name()),
			ErrorHandler:   errorHandler(logger, p.Name()),
		}
	}

	return &Server{
		registry:     reg,
		logger:       logger,
		default_:     defaultT,
		models:       agg,
		maxBodyBytes: maxBodyBytes,
		proxies:      proxies,
	}
}

func (s *Server) modelsHandler(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		writeJSONError(w, http.StatusNotFound, "models_disabled", "/v1/models aggregator is not configured")
		return
	}
	out := s.models.List(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   out,
	})
}

// Handler returns the top-level HTTP handler. It is concurrency-safe and
// can be wrapped with auth/logging middleware by the caller.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthHandler)
	mux.HandleFunc("/ready", s.readyHandler)
	mux.HandleFunc("/v1/models", s.modelsHandler)
	mux.HandleFunc("/", s.proxyHandler)
	return mux
}

func (s *Server) proxyHandler(w http.ResponseWriter, r *http.Request) {
	p := s.registry.ByPrefix(r.URL.Path)
	if p == nil {
		writeJSONError(w, http.StatusNotFound, "no_provider", fmt.Sprintf("no provider matches %q", r.URL.Path))
		return
	}
	rp := s.proxies[p.Name()]
	if rp == nil {
		writeJSONError(w, http.StatusInternalServerError, "no_proxy", "provider has no proxy configured")
		return
	}
	// Cap the request body before handing off to ReverseProxy so a giant
	// streamed POST can't exhaust memory in the upstream RoundTripper or
	// in any provider transport that buffers (e.g. SigV4 SHA256).
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	}
	s.logger.Debug("forwarding", "provider", p.Name(), "path", r.URL.Path)
	rp.ServeHTTP(w, r)
}

func (s *Server) healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) readyHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if len(s.registry.All()) == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ProviderRegistry exposes the registry so other packages (e.g. models)
// can iterate over providers without us re-exporting every method.
func (s *Server) ProviderRegistry() *provider.Registry { return s.registry }

func modifyResponse(logger *slog.Logger, name string) func(*http.Response) error {
	return func(resp *http.Response) error {
		// Honor SSE: if the upstream returns event-stream, strip transfer
		// encodings that buffering middleware might add.
		if isStream(resp) {
			resp.Header.Del("Content-Length")
		}
		logger.Debug("upstream response", "provider", name, "status", resp.StatusCode)
		return nil
	}
}

func errorHandler(logger *slog.Logger, name string) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		// Client cancellations are normal during streaming; log at debug.
		if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) {
			logger.Debug("upstream cancelled", "provider", name, "err", err)
			return
		}
		// MaxBytesReader trips when the client streams a request body larger
		// than the configured cap. Surface that as 413 rather than a generic
		// 502 — it's a client error, not an upstream failure.
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			logger.Warn("request body too large", "provider", name, "limit", maxErr.Limit, "path", r.URL.Path)
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds maximum allowed size")
			return
		}
		logger.Error("upstream error", "provider", name, "err", err, "path", r.URL.Path)
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
	}
}

func isStream(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return ct == "text/event-stream" || ct == "application/vnd.amazon.eventstream"
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    code,
			"message": msg,
		},
	})
}
