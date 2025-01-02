package proxy

import (
	"github.com/robertprast/goop/pkg/engine/azure"
	"github.com/robertprast/goop/pkg/engine/openai"
	"github.com/robertprast/goop/pkg/engine/vertex"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/robertprast/goop/pkg/audit"
	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

// ProxyHandler holds dependencies for the proxy
type ProxyHandler struct {
	Config  *utils.Config
	Logger  *logrus.Logger
	Metrics *Metrics
}

// NewProxyHandler creates a new proxy handler with logging and telemetry
func NewProxyHandler(config *utils.Config, logger *logrus.Logger, metrics *Metrics) http.Handler {
	handler := &ProxyHandler{
		Config:  config,
		Logger:  logger,
		Metrics: metrics,
	}
	var finalHandler http.Handler = http.HandlerFunc(handler.reverseProxy)
	finalHandler = chainMiddlewares(finalHandler, handler.auditMiddleware, handler.engineMiddleware, handler.loggingMiddleware)
	return finalHandler
}

// loggingMiddleware logs each incoming HTTP request and records metrics
func (h *ProxyHandler) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		h.Metrics.RequestsTotal.WithLabelValues(r.Method, r.URL.Path).Inc()

		rec := &StatusRecorder{ResponseWriter: w, StatusCode: http.StatusOK}
		next.ServeHTTP(rec, r)

		duration := time.Since(startTime).Seconds()
		h.Metrics.RequestDuration.WithLabelValues(r.Method, r.URL.Path).Observe(duration)

		h.Logger.Infof("Method: %s, Path: %s, Status: %d, Duration: %.4f seconds", r.Method, r.URL.Path, rec.StatusCode, duration)
	})
}

// auditMiddleware audits each request and records errors if any
func (h *ProxyHandler) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Logger.Infof("Auditing request: %s %s", r.Method, r.URL.Path)
		err := audit.Request(r)
		if err != nil {
			h.Metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "audit_failed").Inc()
			h.Logger.Errorf("Audit failed: %v", err)
			http.Error(w, "Audit failed", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// engineMiddleware selects the appropriate engine based on the request path and records errors
func (h *ProxyHandler) engineMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segments := strings.SplitN(r.URL.Path, "/", 3)
		if len(segments) < 2 {
			h.Metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "invalid_path").Inc()
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		firstPathSegment := segments[1]
		h.Logger.Infof("First path segment: %s", firstPathSegment)

		// Check if config has engine
		engineConfig, ok := h.Config.Engines[firstPathSegment]
		if !ok {
			h.Metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "engine_not_found").Inc()
			http.Error(w, "Engine not found", http.StatusNotFound)
			return
		}

		var eng engine.Engine
		var err error
		switch firstPathSegment {
		case "openai":
			eng, err = openai.NewOpenAIEngineWithConfig(engineConfig)
		case "azure":
			eng, err = azure.NewAzureOpenAIEngineWithConfig(engineConfig)
		case "bedrock":
			eng, err = bedrock.NewBedrockEngine(engineConfig)
		case "vertex":
			eng, err = vertex.NewVertexEngine(engineConfig)
		default:
			h.Metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "engine_not_found").Inc()
			http.Error(w, "Engine not found", http.StatusNotFound)
			return
		}

		if err != nil {
			h.Metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "engine_init_failed").Inc()
			h.Logger.Errorf("Error selecting engine: %v", err)
			http.Error(w, "Error selecting engine", http.StatusInternalServerError)
			return
		}

		if !eng.IsAllowedPath(strings.TrimPrefix(r.URL.Path, eng.Name())) {
			h.Metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "forbidden").Inc()
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		h.Logger.Infof("Selected engine: %s", eng.Name())
		ctx := engine.ContextWithEngine(r.Context(), eng)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// reverseProxy handles the actual proxying of requests
func (h *ProxyHandler) reverseProxy(w http.ResponseWriter, r *http.Request) {
	eng := engine.FromContext(r.Context())
	if eng == nil {
		h.Metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "engine_missing").Inc()
		http.Error(w, "Engine not found", http.StatusInternalServerError)
		return
	}

	eng.ModifyRequest(r)

	proxy := &httputil.ReverseProxy{
		Director:       func(req *http.Request) {},
		ModifyResponse: audit.Response,
		Transport:      http.DefaultTransport,
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.Metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "streaming_not_supported").Inc()
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	proxy.ServeHTTP(w, r)
	flusher.Flush()
}
