package openai_proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

type OpenAIProxyHandler struct {
	config utils.Config
}

func NewHandler(config utils.Config) http.Handler {
	return &OpenAIProxyHandler{config: config}
}

func (h *OpenAIProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Read and parse the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	var reqBody map[string]interface{}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		http.Error(w, "Error parsing request body", http.StatusBadRequest)
		return
	}

	switch r.URL.Path {
	case "/openai-proxy/v1/chat/completions":
		h.handleChatCompletions(w, r, reqBody)
	default:
		http.Error(w, "Unsupported path", http.StatusNotFound)
	}
}

func (h *OpenAIProxyHandler) handleChatCompletions(w http.ResponseWriter, r *http.Request, reqBody map[string]interface{}) {
	eng, err := h.selectEngine(reqBody["model"].(string))
	if err != nil {
		logrus.Errorf("Error getting engine: %v", err)
		http.Error(w, "Error selecting engine", http.StatusInternalServerError)
		return
	}
	logrus.Infof("HI ")

	var stream bool
	logrus.Infof("Stream: %v", reqBody)

	if rawStream, ok := reqBody["stream"]; ok && rawStream == true {
		stream = true
	}

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
		return bedrock.NewBedrockEngine(h.config.Engines["bedrock"])
	case strings.HasPrefix(model, "vertex/"):
		return nil, fmt.Errorf("vertex AI not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported model: %s", model)
	}
}
