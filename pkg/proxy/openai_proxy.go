package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robertprast/goop/pkg/audit"
	"github.com/robertprast/goop/pkg/openai_schema"
	bedrockproxy "github.com/robertprast/goop/pkg/transformers/bedrock"
	geminiproxy "github.com/robertprast/goop/pkg/transformers/gemini"
	openaiproxy "github.com/robertprast/goop/pkg/transformers/openai"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

type Response struct {
	Object string                `json:"object"`
	Data   []openai_schema.Model `json:"data"`
}

// OpenAIProxyEngine defines the interface for OpenAI proxy engines
type OpenAIProxyEngine interface {
	HandleChatCompletionRequest(ctx context.Context, model string, stream bool, transformedBody []byte) (*http.Response, error)
	SendChatCompletionResponse(bedrockResp *http.Response, w http.ResponseWriter, stream bool) error
	TransformChatCompletionRequest(reqBody openai_schema.IncomingChatCompletionRequest) ([]byte, error)
}

// OpenAIProxyHandler holds dependencies for the OpenAI proxy
type OpenAIProxyHandler struct {
	config      *utils.Config
	logger      *logrus.Logger
	engineCache *EngineCache
}

// NewHandler creates a new OpenAI proxy handler with logging and telemetry
func NewHandler(config *utils.Config, logger *logrus.Logger) http.Handler {
	engineCache := NewEngineCache(config, logger)
	engineCache.StartCleanupRoutine()

	handler := &OpenAIProxyHandler{
		config:      config,
		logger:      logger,
		engineCache: engineCache,
	}
	var finalHandler http.Handler = http.HandlerFunc(handler.ServeHTTP)
	finalHandler = chainMiddlewares(finalHandler, handler.auditMiddleware, handler.loggingMiddleware, handler.corsMiddleware)
	return finalHandler
}

// chainMiddlewares applies the given middlewares to the final handler
func chainMiddlewares(finalHandler http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		finalHandler = middlewares[i](finalHandler)
	}
	return finalHandler
}

// auditMiddleware audits each request and records errors if any
func (h *OpenAIProxyHandler) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.logger.Infof("Auditing request: %s %s", r.Method, r.URL.Path)
		err := audit.Request(r)
		if err != nil {
			h.logger.Errorf("Audit failed: %v", err)
			http.Error(w, "Audit failed", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs each incoming HTTP request
func (h *OpenAIProxyHandler) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(startTime).Seconds()
		h.logger.Infof("Method: %s, Path: %s, Duration: %.4f seconds",
			r.Method, r.URL.Path, duration)
	})
}

// corsMiddleware adds CORS headers to responses
func (h *OpenAIProxyHandler) corsMiddleware(next http.Handler) http.Handler {
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

// ServeHTTP handles incoming HTTP requests for the OpenAI proxy
func (h *OpenAIProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	// Read and parse the request body
	h.logger.Infof("Transforming path in openai proxy %s", r.URL.Path)

	switch r.URL.Path {
	case "/openai-proxy/v1/models":
		h.handleModels(w, r)
	case "/openai-proxy/v1/chat/completions":
		if r.Method == http.MethodPost {
			h.handleChatCompletions(w, r)
		} else {
			http.Error(w, "Unsupported method", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(w, "Unsupported path", http.StatusNotFound)
	}

	duration := time.Since(startTime).Seconds()
	h.logger.Infof("Request processed in %f seconds", duration)
}

// handleModels handles the /openai-proxy/v1/models endpoint
func (h *OpenAIProxyHandler) handleModels(w http.ResponseWriter, r *http.Request) {
	h.logger.Infof("Fetching model list")
	models := Response{
		Object: "list",
		Data:   []openai_schema.Model{}}

	// Get available engines (only those with valid credentials)
	availableEngines := h.engineCache.GetAvailableEngines()

	// Add models from available engines
	for _, engineType := range availableEngines {
		switch engineType {
		case "bedrock":
			h.logger.Infof("Adding Bedrock models (engine available)")
			proxyEngine, err := h.engineCache.GetEngine("bedrock", "bedrock/models")
			if err != nil {
				h.logger.Errorf("Error getting bedrock engine: %v", err)
			} else {
				// Extract underlying bedrock engine from proxy
				if bedrockProxy, ok := proxyEngine.(*bedrockproxy.BedrockProxy); ok {
					bModels, err := bedrockProxy.BedrockEngine.ListModels()
					if err != nil {
						h.logger.Errorf("Error listing bedrock models: %v", err)
					} else {
						h.logger.Infof("Got %d models from bedrock", len(bModels))
						models.Data = append(models.Data, bModels...)
					}
				}
			}

		case "openai":
			h.logger.Infof("Adding OpenAI models (engine available)")
			proxyEngine, err := h.engineCache.GetEngine("openai", "openai/models")
			if err != nil {
				h.logger.Errorf("Error getting openai engine: %v", err)
			} else {
				// Extract underlying openai engine from proxy
				if openaiProxy, ok := proxyEngine.(*openaiproxy.OpenAIProxy); ok {
					oModels, err := openaiProxy.OpenAIEngine.ListModels()
					if err != nil {
						h.logger.Errorf("Error listing openai models: %v", err)
					} else {
						h.logger.Infof("Got %d models from openai", len(oModels))
						models.Data = append(models.Data, oModels...)
					}
				}
			}

		case "gemini":
			h.logger.Infof("Adding Gemini models (engine available)")
			proxyEngine, err := h.engineCache.GetEngine("gemini", "gemini/models")
			if err != nil {
				h.logger.Errorf("Error getting gemini engine: %v", err)
			} else {
				// Extract underlying gemini engine from proxy
				if geminiProxy, ok := proxyEngine.(*geminiproxy.GeminiProxy); ok {
					gModels, err := geminiProxy.GeminiEngine.ListModels()
					if err != nil {
						h.logger.Errorf("Error listing gemini models: %v", err)
					} else {
						h.logger.Infof("Got %d models from gemini", len(gModels))
						models.Data = append(models.Data, gModels...)
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(models)
	if err != nil {
		h.logger.Errorf("Error encoding models response: %v", err)
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
		return
	}
}

// handleChatCompletions handles the /openai-proxy/v1/chat/completions endpoint
func (h *OpenAIProxyHandler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	// Read the entire body first
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Errorf("Error reading request body: %v", err)
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			h.logger.Errorf("Error closing body: %v", err)
		}
	}(r.Body)

	// Log the raw body for debugging
	h.logger.Debugf("Request body raw!!: %s", string(body))

	// Unmarshal the request body into the struct
	var reqBody openai_schema.IncomingChatCompletionRequest
	if err := json.Unmarshal(body, &reqBody); err != nil {
		h.logger.Errorf("Error parsing request body: %v", err)
		http.Error(w, "Error parsing request body", http.StatusBadRequest)
		return
	}
	h.logger.Info("Parsed request body: ", reqBody)

	h.logger.Debugf("Request body after transform: %+v", reqBody)

	h.handleChatCompletionsInternal(w, r, reqBody, reqBody.Stream)
}

// handleChatCompletionsInternal processes the chat completions request
func (h *OpenAIProxyHandler) handleChatCompletionsInternal(w http.ResponseWriter, r *http.Request, reqBody openai_schema.IncomingChatCompletionRequest, stream bool) {
	proxyEngine, err := h.selectEngine(reqBody.Model)
	if err != nil {
		h.logger.Errorf("Error getting engine: %v", err)

		// Return appropriate status code based on error type
		if strings.Contains(err.Error(), "not available") || strings.Contains(err.Error(), "not configured") {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else if strings.Contains(err.Error(), "unsupported model") {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.Error(w, "Error selecting engine", http.StatusInternalServerError)
		}
		return
	}

	transformedBody, err := proxyEngine.TransformChatCompletionRequest(reqBody)
	if err != nil {
		h.logger.Infof("Error transforming request: %v", err)
		http.Error(w, "Error transforming request", http.StatusInternalServerError)
		return
	}
	h.logger.Debugf("Transformed request: %s", string(transformedBody))

	resp, err := proxyEngine.HandleChatCompletionRequest(r.Context(), reqBody.Model, stream, transformedBody)
	if err != nil {
		h.logger.Infof("Error processing request: %v", err)
		http.Error(w, fmt.Sprintf("Error processing request: %v", err), http.StatusInternalServerError)
		return
	}

	if err := proxyEngine.SendChatCompletionResponse(resp, w, stream); err != nil {
		h.logger.Infof("Error sending response: %v", err)
		http.Error(w, fmt.Sprintf("Error sending response: %v", err), http.StatusInternalServerError)
		return
	}

	duration := time.Since(time.Now()).Seconds()
	h.logger.Infof("Chat completion processed in %f seconds", duration)
}

// selectEngine selects the appropriate engine based on the model and records errors
func (h *OpenAIProxyHandler) selectEngine(model string) (OpenAIProxyEngine, error) {
	var engineType string

	// Get available engines (only those with valid credentials)
	availableEngines := h.engineCache.GetAvailableEngines()
	isEngineAvailable := func(engine string) bool {
		for _, available := range availableEngines {
			if available == engine {
				return true
			}
		}
		return false
	}

	switch {
	case strings.HasPrefix(model, "openai/"):
		engineType = "openai"
		if !isEngineAvailable(engineType) {
			return nil, fmt.Errorf("OpenAI engine not available - check API key configuration")
		}
		h.logger.Info("Selecting OpenAI engine")
	case strings.HasPrefix(model, "bedrock/"):
		engineType = "bedrock"
		if !isEngineAvailable(engineType) {
			return nil, fmt.Errorf("Bedrock engine not available - check AWS credentials configuration")
		}
		h.logger.Info("Selecting Bedrock engine")
	case strings.HasPrefix(model, "gemini/"):
		engineType = "gemini"
		if !isEngineAvailable(engineType) {
			return nil, fmt.Errorf("Gemini engine not available - check API key configuration")
		}
		h.logger.Info("Selecting Gemini AI engine")
	// If no prefix is provided, try to infer the engine from available configurations
	default:
		// Check if it's a known OpenAI model (gpt-* models)
		if strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "text-") || strings.HasPrefix(model, "davinci") {
			if isEngineAvailable("openai") {
				engineType = "openai"
				h.logger.Infof("Detected OpenAI model %s, selecting OpenAI engine", model)
			} else {
				return nil, fmt.Errorf("OpenAI engine not available for model %s - check API key configuration", model)
			}
		} else if strings.HasPrefix(model, "gemini-") {
			if isEngineAvailable("gemini") {
				engineType = "gemini"
				h.logger.Infof("Detected Gemini model %s, selecting Gemini engine", model)
			} else {
				return nil, fmt.Errorf("Gemini engine not available for model %s - check API key configuration", model)
			}
		} else {
			return nil, fmt.Errorf("unsupported model: %s. Use prefixes like openai/, bedrock/, or gemini/ to specify the engine", model)
		}
	}

	// Use cached engine
	proxyEngine, err := h.engineCache.GetEngine(engineType, model)
	if err != nil {
		h.logger.Errorf("Error getting cached engine for %s: %v", engineType, err)
		return nil, err
	}

	return proxyEngine, nil
}
