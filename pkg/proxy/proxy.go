package proxy

import (
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/robertprast/goop/pkg/engine/azure"
	"github.com/robertprast/goop/pkg/engine/gemini"
	"github.com/robertprast/goop/pkg/engine/openai"

	"github.com/robertprast/goop/pkg/audit"
	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

// ProxyHandler holds dependencies for the proxy
type ProxyHandler struct {
	Config      *utils.Config
	Logger      *logrus.Logger
	EngineCache *EngineCache
}

// NewProxyHandler creates a new proxy handler with logging and telemetry
func NewProxyHandler(config *utils.Config, logger *logrus.Logger) http.Handler {
	engineCache := NewEngineCache(config, logger)
	handler := &ProxyHandler{
		Config:      config,
		Logger:      logger,
		EngineCache: engineCache,
	}
	var finalHandler http.Handler = http.HandlerFunc(handler.reverseProxy)
	finalHandler = chainMiddlewares(finalHandler, handler.auditMiddleware, handler.engineMiddleware, handler.loggingMiddleware, handler.corsMiddleware)
	return finalHandler
}

// loggingMiddleware logs each incoming HTTP request
func (h *ProxyHandler) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(startTime).Seconds()
		h.Logger.Infof("Method: %s, Path: %s, Duration: %.4f seconds", r.Method, r.URL.Path, duration)
	})
}

// auditMiddleware audits each request and records errors if any
func (h *ProxyHandler) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Logger.Infof("Auditing request: %s %s", r.Method, r.URL.Path)
		err := audit.Request(r)
		if err != nil {
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
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		firstPathSegment := segments[1]
		h.Logger.Infof("First path segment: %s", firstPathSegment)

		// Check if engine is available (has valid credentials)
		availableEngines := h.EngineCache.GetAvailableEngines()
		engineAvailable := false
		for _, engine := range availableEngines {
			if engine == firstPathSegment {
				engineAvailable = true
				break
			}
		}
		if !engineAvailable {
			h.Logger.Warnf("Engine %s not available (no valid credentials configured)", firstPathSegment)
			http.Error(w, "Engine not available", http.StatusNotFound)
			return
		}

		engineConfig := h.Config.Engines[firstPathSegment]

		var eng engine.Engine
		var err error
		switch firstPathSegment {
		case "openai":
			eng, err = openai.NewOpenAIEngineWithConfig(engineConfig)
		case "azure":
			eng, err = azure.NewAzureOpenAIEngineWithConfig(engineConfig)
		case "bedrock":
			eng, err = bedrock.NewBedrockEngine(engineConfig)
		case "gemini":
			eng, err = gemini.NewGeminiEngine(engineConfig)
		default:
			http.Error(w, "Engine not found", http.StatusNotFound)
			return
		}

		if err != nil {
			h.Logger.Errorf("Error selecting engine: %v", err)
			http.Error(w, "Error selecting engine", http.StatusInternalServerError)
			return
		}

		if !eng.IsAllowedPath(strings.TrimPrefix(r.URL.Path, eng.Name())) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		h.Logger.Infof("Selected engine: %s", eng.Name())
		ctx := engine.ContextWithEngine(r.Context(), eng)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// corsMiddleware adds CORS headers to responses
func (h *ProxyHandler) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// reverseProxy handles the actual proxying of requests
func (h *ProxyHandler) reverseProxy(w http.ResponseWriter, r *http.Request) {
	eng := engine.FromContext(r.Context())
	if eng == nil {
		http.Error(w, "Engine not found", http.StatusInternalServerError)
		return
	}
	h.Logger.Infof("Transforming path %s", r.URL.Path)
	h.Logger.Infof("Transforming request for %s", eng.Name())
	h.Logger.Infof("using engine %s", eng.Name())
	h.Logger.Infof("Request body: %s", r.Body)
	defer r.Body.Close()

	eng.ModifyRequest(r)

	proxy := &httputil.ReverseProxy{
		Director:       func(req *http.Request) {},
		ModifyResponse: audit.Response,
		Transport:      http.DefaultTransport,
		FlushInterval:  -1, // Flush immediately as data arrives
	}

	_, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	proxy.ServeHTTP(w, r)
}
