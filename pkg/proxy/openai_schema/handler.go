package openai_proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/robertprast/goop/pkg/proxy/openai_schema/types"
	bedrock_proxy "github.com/robertprast/goop/pkg/transformers/bedrock"
	"io"
	"net/http"
	"strings"

	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

var _ OpenAIProxyEngine = (*bedrock_proxy.BedrockProxy)(nil)

type OpenAIProxyEngine interface {
	HandleChatCompletionRequest(ctx context.Context, transformedBody []byte, stream bool) (*http.Response, error)
	SendChatCompletionResponse(bedrockResp *http.Response, w http.ResponseWriter, stream bool) error
	TransformChatCompletionRequest(reqBody openai_types.IncomingChatCompletionRequest) ([]byte, error)
}

type OpenAIProxyHandler struct {
	config utils.Config
}

func NewHandler(config utils.Config) http.Handler {
	return &OpenAIProxyHandler{config: config}
}

type Model struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type Response struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

func (h *OpenAIProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Read and parse the request body
	logrus.Infof("Transforming path %s", r.URL.Path)

	switch r.URL.Path {
	case "/openai_schema-engine_proxy/v1/models":
		logrus.Infof("Fetching model list")
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
			return
		}
		return
	case "/openai_schema-engine_proxy/v1/chat/completions":
		if r.Method == http.MethodPost {
			// Read the entire body first
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "Error reading request body", http.StatusInternalServerError)
				return
			}
			defer func(Body io.ReadCloser) {
				err := Body.Close()
				if err != nil {
					logrus.Errorf("Error closing body: %v", err)
				}
			}(r.Body)

			// log the body for debugging
			logrus.Infof("Request body raw: %s", string(body))

			// Now unmarshal the request body into the struct
			var reqBody openai_types.IncomingChatCompletionRequest
			if err := json.Unmarshal(body, &reqBody); err != nil {
				logrus.Errorf("Error parsing request body: %v", err)
				http.Error(w, "Error parsing request body", http.StatusBadRequest)
				return
			}

			logrus.Infof("Request body after transform: %v", reqBody)
			h.handleChatCompletions(w, r, reqBody, reqBody.Stream)
		} else {
			http.Error(w, "Unsupported method", http.StatusMethodNotAllowed)
		}

	default:
		http.Error(w, "Unsupported path", http.StatusNotFound)
	}
}

func (h *OpenAIProxyHandler) handleChatCompletions(w http.ResponseWriter, r *http.Request, reqBody openai_types.IncomingChatCompletionRequest, stream bool) {
	proxyEngine, err := h.selectEngine(reqBody.Model)
	if err != nil {
		logrus.Errorf("Error getting engine: %v", err)
		http.Error(w, "Error selecting engine", http.StatusInternalServerError)
		return
	}
	logrus.Infof("HI ")

	logrus.Infof("Stream: %v", reqBody)

	logrus.Infof("Stream: %v", stream)

	transformedBody, err := proxyEngine.TransformChatCompletionRequest(reqBody)
	if err != nil {
		logrus.Infof("Error transforming request: %v", err)
		http.Error(w, "Error transforming request", http.StatusInternalServerError)
		return
	}
	logrus.Debugf("Transformed request: %v", string(transformedBody))

	resp, err := proxyEngine.HandleChatCompletionRequest(r.Context(), transformedBody, stream)
	if err != nil {
		logrus.Infof("Error processing request %v", err)
		http.Error(w, fmt.Sprintf("Error processing request: %v", err), http.StatusInternalServerError)
		return
	}

	if err := proxyEngine.SendChatCompletionResponse(resp, w, stream); err != nil {
		logrus.Infof("Error sending request %v", err)
		http.Error(w, fmt.Sprintf("Error sending response: %v", err), http.StatusInternalServerError)
		return
	}
}

func (h *OpenAIProxyHandler) selectEngine(model string) (OpenAIProxyEngine, error) {
	switch {
	case strings.HasPrefix(model, "bedrock/"):
		logrus.Info("Selecting bedrock engine")
		bedrockEngine, err := bedrock.NewBedrockEngine(h.config.Engines["bedrock"])
		if err != nil {
			logrus.Errorf("Error creating bedrock engine: %v", err)
			return nil, err
		}
		return &bedrock_proxy.BedrockProxy{
			BedrockEngine: bedrockEngine,
		}, nil
	case strings.HasPrefix(model, "vertex/"):
		return nil, fmt.Errorf("vertex AI not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported model: %s", model)
	}
}
