package openai_proxy

import (
	"encoding/json"
	"fmt"
	bedrock2 "github.com/robertprast/goop/pkg/openai_proxy/transformers/bedrock"
	"io"
	"net/http"
	"strings"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	openai_types "github.com/robertprast/goop/pkg/openai_proxy/types"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

type OpenAIProxyEngine interface {
	SendChatCompletionResponse(bedrockResp *http.Response, w http.ResponseWriter, stream bool) error
	TransformChatCompletionRequest(reqBody openai_types.InconcomingChatCompletionRequest) ([]byte, error)
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
	case "/openai-proxy/v1/models":
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
	case "/openai-proxy/v1/chat/completions":
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

				}
			}(r.Body)

			// log the body for debugging
			logrus.Infof("Request body raw: %s", string(body))

			// Now unmarshal the request body into the struct
			var reqBody openai_types.InconcomingChatCompletionRequest
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

func (h *OpenAIProxyHandler) handleChatCompletions(w http.ResponseWriter, r *http.Request, reqBody openai_types.InconcomingChatCompletionRequest, stream bool) {
	eng, err := h.selectEngine(reqBody.Model)
	if err != nil {
		logrus.Errorf("Error getting engine: %v", err)
		http.Error(w, "Error selecting engine", http.StatusInternalServerError)
		return
	}
	logrus.Infof("HI ")

	logrus.Infof("Stream: %v", reqBody)

	logrus.Infof("Stream: %v", stream)

	proxyEngine, ok := eng.(engine.OpenAIProxyEngine)
	if !ok {
		logrus.Infof("Engine does not support chat completionst: %v", err)
		http.Error(w, "Engine does not support chat completions", http.StatusInternalServerError)
		return
	}

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

func (h *OpenAIProxyHandler) selectEngine(model string) (engine.Engine, error) {
	switch {
	case strings.HasPrefix(model, "bedrock/"):
		logrus.Info("Selecting bedrock engine")
		bedrock, err := bedrock.NewBedrockEngine(h.config.Engines["bedrock"])
		if err != nil {
			return nil, err
		}
		return bedrock2.BedrockProxy{
			BedrockEngine: bedrock,
		}, nil
	case strings.HasPrefix(model, "vertex/"):
		return nil, fmt.Errorf("vertex AI not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported model: %s", model)
	}
}
