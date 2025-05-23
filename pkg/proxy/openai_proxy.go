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

	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/engine/vertex"
	bedrockproxy "github.com/robertprast/goop/pkg/transformers/bedrock"
	vertexproxy "github.com/robertprast/goop/pkg/transformers/vertex"
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
	config  *utils.Config
	logger  *logrus.Logger
	metrics *OpenaiProxyMetrics
}

// NewHandler creates a new OpenAI proxy handler with logging and telemetry
func NewHandler(config *utils.Config, logger *logrus.Logger, metrics *OpenaiProxyMetrics) http.Handler {
	handler := &OpenAIProxyHandler{
		config:  config,
		logger:  logger,
		metrics: metrics,
	}
	var finalHandler http.Handler = http.HandlerFunc(handler.ServeHTTP)
	finalHandler = chainMiddlewares(finalHandler, handler.auditMiddleware, handler.loggingMiddleware)
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
			h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "audit_failed").Inc()
			h.logger.Errorf("Audit failed: %v", err)
			http.Error(w, "Audit failed", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs each incoming HTTP request and records metrics
func (h *OpenAIProxyHandler) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		h.metrics.RequestsTotal.WithLabelValues(r.Method, r.URL.Path).Inc()

		rec := &StatusRecorder{ResponseWriter: w, StatusCode: http.StatusOK}
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
	h.logger.Infof("Transforming path in openai proxy %s", r.URL.Path)

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
		Data:   []openai_schema.Model{}}

	logrus.Infof(h.config.Engines["bedrock"])
	bedrockEngine, err := bedrock.NewBedrockEngine(h.config.Engines["bedrock"])
	if err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "bedrock model list error").Inc()
		h.logger.Errorf("Error listing bedrock models: %v", err)
		http.Error(w, "Error listing bedrock models", http.StatusInternalServerError)
		return
	}
	bModels, err := bedrockEngine.ListModels()
	if err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "bedrock model list error").Inc()
		h.logger.Errorf("Error listing bedrock models: %v", err)
		http.Error(w, "Error listing bedrock models", http.StatusInternalServerError)
		return
	}

	logrus.Infof("Got the models from bedrock %v", bModels)
	models.Data = append(models.Data, bModels...)

	vertexEngine, err := vertex.NewVertexEngine(h.config.Engines["vertex"])
	if err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "vertex model list error").Inc()
		h.logger.Errorf("Error listing vertex models: %v", err)
		http.Error(w, "Error listing vertex models", http.StatusInternalServerError)
		return
	}
	vModels, err := vertexEngine.ListModels()
	if err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "vertex model list error").Inc()
		h.logger.Errorf("Error listing vertex models: %v", err)
		http.Error(w, "Error listing vertex models", http.StatusInternalServerError)
		return
	}
	logrus.Infof("Got the models from vertex %v", vModels)
	models.Data = append(models.Data, vModels...)

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(models)
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
	h.logger.Debugf("Request body raw!!: %s", string(body))

	// Unmarshal the request body into the struct
	var reqBody openai_schema.IncomingChatCompletionRequest
	if err := json.Unmarshal(body, &reqBody); err != nil {
		h.metrics.ErrorsTotal.WithLabelValues(r.Method, r.URL.Path, "unmarshal_error").Inc()
		h.logger.Errorf("Error parsing request body: %v", err)
		http.Error(w, "Error parsing request body", http.StatusBadRequest)
		return
	}
	h.logger.Info("Parsed request body: ", reqBody)

	h.logger.Debugf("Request body after transform: %+v", reqBody)
	h.metrics.ChatCompletions.WithLabelValues(reqBody.Model).Inc()

	h.handleChatCompletionsInternal(w, r, reqBody, reqBody.Stream)
}

// handleChatCompletionsInternal processes the chat completions request
func (h *OpenAIProxyHandler) handleChatCompletionsInternal(w http.ResponseWriter, r *http.Request, reqBody openai_schema.IncomingChatCompletionRequest, stream bool) {
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
		h.logger.Info("Selecting Vertex AI engine")
		vertexEngine, err := vertex.NewVertexEngine(h.config.Engines["vertex"])
		if err != nil {
			h.metrics.ErrorsTotal.WithLabelValues("vertex", model, "engine_init_error").Inc()
			h.logger.Errorf("Error creating Vertex engine: %v", err)
			return nil, err
		}
		return &vertexproxy.VertexProxy{
			VertexEngine: vertexEngine,
		}, nil
	default:
		h.metrics.ErrorsTotal.WithLabelValues("unknown", model, "unsupported_model").Inc()
		return nil, fmt.Errorf("unsupported model: %s", model)
	}
}
