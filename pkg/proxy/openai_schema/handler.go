package openai_proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/proxy"
	openai_types "github.com/robertprast/goop/pkg/proxy/openai_schema/types"
	bedrockproxy "github.com/robertprast/goop/pkg/transformers/bedrock"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

// Middleware defines the signature for middleware functions
type Middleware func(http.Handler) http.Handler

// Model represents an OpenAI model
type Model struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// Response represents the response structure for models endpoint
type Response struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// OpenAIProxyEngine defines the interface for OpenAI proxy engines
type OpenAIProxyEngine interface {
	HandleChatCompletionRequest(ctx context.Context, model string, stream bool, transformedBody []byte) (*http.Response, error)
	SendChatCompletionResponse(bedrockResp *http.Response, w http.ResponseWriter, stream bool) error
	TransformChatCompletionRequest(reqBody openai_types.IncomingChatCompletionRequest) ([]byte, error)
}

// OpenAIProxyHandler holds dependencies for the OpenAI proxy
type OpenAIProxyHandler struct {
	config  *utils.Config
	logger  *logrus.Logger
	metrics *proxy.OpenaiProxyMetrics
}

// NewHandler creates a new OpenAI proxy handler with logging and telemetry
func NewHandler(config *utils.Config, logger *logrus.Logger, metrics *proxy.OpenaiProxyMetrics) http.Handler {
	handler := &OpenAIProxyHandler{
		config:  config,
		logger:  logger,
		metrics: metrics,
	}
	var finalHandler http.Handler = http.HandlerFunc(handler.ServeHTTP)

	// Chain middlewares: logging -> telemetry (if any additional middleware is needed)
	// Currently, logging and telemetry are handled within the handler methods
	// You can add more middlewares here if necessary
	finalHandler = chainMiddlewares(finalHandler, handler.loggingMiddleware)

	return finalHandler
}

// chainMiddlewares applies the given middlewares to the final handler
func chainMiddlewares(finalHandler http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		finalHandler = middlewares[i](finalHandler)
	}
	return finalHandler
}

// loggingMiddleware logs each incoming HTTP request and records metrics
func (h *OpenAIProxyHandler) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		h.metrics.RequestsTotal.WithLabelValues(r.Method, r.URL.Path).Inc()

		// Capture the status code
		rec := &proxy.StatusRecorder{ResponseWriter: w, StatusCode: http.StatusOK}
		next.ServeHTTP(rec, r)

		duration := time.Since(startTime).Seconds()
		h.metrics.RequestDuration.WithLabelValues(r.Method, r.URL.Path).Observe(duration)

		h.logger.Infof("Method: %s, Path: %s, Status: %d, Duration: %.4f seconds",
			r.Method, r.URL.Path, rec.StatusCode, duration)
	})
}

// ServeHTTP handles incoming HTTP requests for the OpenAI proxy
func (h *OpenAIProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.metrics.RequestsTotal.WithLabelValues(r.Method, r.URL.Path).Inc()

	startTime := time.Now()

	// Read and parse the request body
	h.logger.Infof("Transforming path %s", r.URL.Path)

	switch r.URL.Path {
	case "/openai-proxy/v1/models":
		h.handleModels(w, r)
	case "/openai-proxy/v1/chat/completions":
		if r.Method == http.MethodPost {
			h.handleChatCompletions(w, r)
		} else {
			h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "method_not_allowed").Inc()
			http.Error(w, "Unsupported method", http.StatusMethodNotAllowed)
		}
	default:
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "unsupported_path").Inc()
		http.Error(w, "Unsupported path", http.StatusNotFound)
	}

	duration := time.Since(startTime).Seconds()
	h.metrics.RequestDuration.WithLabelValues(r.Method, r.URL.Path).Observe(duration)
}

// handleModels handles the /openai-proxy/v1/models endpoint
func (h *OpenAIProxyHandler) handleModels(w http.ResponseWriter, r *http.Request) {
	h.logger.Infof("Fetching model list")
	models := Response{
		Object: "list",
		Data: []Model{
			{
				ID:      "bedrock/us.anthropic.claude-3-haiku-20240307-v1:0",
				Name:    "claude-3-haiku",
				Object:  "model",
				Created: 1686935002,
				OwnedBy: "amazon",
			},
			{
				ID:      "bedrock/us.anthropic.claude-3-5-sonnet-20240620-v1:0",
				Name:    "claude-3-5-sonnet",
				Object:  "model",
				Created: 1686935002,
				OwnedBy: "amazon",
			},
			{
				ID:      "bedrock/us.meta.llama3-2-11b-instruct-v1:0",
				Name:    "llama3.2-11b",
				Object:  "model",
				Created: 1686935002,
				OwnedBy: "amazon",
			},
			{
				ID:      "bedrock/us.meta.llama3-2-1b-instruct-v1:0",
				Name:    "llama3.2-1b",
				Object:  "model",
				Created: 1686935002,
				OwnedBy: "amazon",
			},
			{
				ID:      "bedrock/us.meta.llama3-2-3b-instruct-v1:0",
				Name:    "llama3.2-3b",
				Object:  "model",
				Created: 1686935002,
				OwnedBy: "amazon",
			},
			{
				ID:      "bedrock/us.meta.llama3-2-90b-instruct-v1:0",
				Name:    "llama3.2-90b",
				Object:  "model",
				Created: 1686935002,
				OwnedBy: "amazon",
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(models)
	if err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "encode_error").Inc()
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
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "read_body_error").Inc()
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
	h.logger.Debugf("Request body raw: %s", string(body))

	// Unmarshal the request body into the struct
	var reqBody openai_types.IncomingChatCompletionRequest
	if err := json.Unmarshal(body, &reqBody); err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "unmarshal_error").Inc()
		h.logger.Errorf("Error parsing request body: %v", err)
		http.Error(w, "Error parsing request body", http.StatusBadRequest)
		return
	}

	h.logger.Debugf("Request body after transform: %+v", reqBody)
	h.metrics.ChatCompletions.WithLabelValues(reqBody.Model).Inc()

	h.handleChatCompletionsInternal(w, r, reqBody, reqBody.Stream)
}

// handleChatCompletionsInternal processes the chat completions request
func (h *OpenAIProxyHandler) handleChatCompletionsInternal(w http.ResponseWriter, r *http.Request, reqBody openai_types.IncomingChatCompletionRequest, stream bool) {
	proxyEngine, err := h.selectEngine(reqBody.Model)
	if err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "engine_selection_error").Inc()
		h.logger.Errorf("Error getting engine: %v", err)
		http.Error(w, "Error selecting engine", http.StatusInternalServerError)
		return
	}

	transformedBody, err := proxyEngine.TransformChatCompletionRequest(reqBody)
	if err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "transform_error").Inc()
		h.logger.Infof("Error transforming request: %v", err)
		http.Error(w, "Error transforming request", http.StatusInternalServerError)
		return
	}
	h.logger.Debugf("Transformed request: %s", string(transformedBody))

	resp, err := proxyEngine.HandleChatCompletionRequest(r.Context(), reqBody.Model, stream, transformedBody)
	if err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "handle_request_error").Inc()
		h.logger.Infof("Error processing request: %v", err)
		http.Error(w, fmt.Sprintf("Error processing request: %v", err), http.StatusInternalServerError)
		return
	}

	if err := proxyEngine.SendChatCompletionResponse(resp, w, stream); err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "send_response_error").Inc()
		h.logger.Infof("Error sending response: %v", err)
		http.Error(w, fmt.Sprintf("Error sending response: %v", err), http.StatusInternalServerError)
		return
	}

	duration := time.Since(time.Now()).Seconds()
	h.metrics.ChatCompletionDurations.WithLabelValues(reqBody.Model).Observe(duration)
}

// selectEngine selects the appropriate engine based on the model and records errors
func (h *OpenAIProxyHandler) selectEngine(model string) (OpenAIProxyEngine, error) {
	switch {
	case strings.HasPrefix(model, "bedrock/"):
		h.logger.Info("Selecting Bedrock engine")
		bedrockEngine, err := bedrock.NewBedrockEngine(h.config.Engines["bedrock"])
		if err != nil {
			h.metrics.ErrorsTotal.WithLabelValues("bedrock", model, "engine_init_error").Inc()
			h.logger.Errorf("Error creating Bedrock engine: %v", err)
			return nil, err
		}
		return &bedrockproxy.BedrockProxy{
			BedrockEngine: bedrockEngine,
		}, nil
	case strings.HasPrefix(model, "vertex/"):
		h.metrics.ErrorsTotal.WithLabelValues("vertex", model, "not_implemented").Inc()
		return nil, fmt.Errorf("vertex AI not yet implemented")
	default:
		h.metrics.ErrorsTotal.WithLabelValues("unknown", model, "unsupported_model").Inc()
		return nil, fmt.Errorf("unsupported model: %s", model)
	}
}
