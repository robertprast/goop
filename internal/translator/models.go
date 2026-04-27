package translator

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/robertprast/goop/internal/provider"
)

// ModelsHandler serves /bedrock-translate/v1/models (and the legacy
// /openai-bedrock/v1/models alias). The translator is a single chat-completions
// endpoint, not a full provider, so it has no entry in the proxy registry —
// without this handler the path falls through to the catch-all proxyHandler
// and 404s with "no provider matches", which trips up OpenAI-shape clients
// that probe /v1/models on connection setup (open-webui, LiteLLM, etc.).
//
// We surface the same model list the native bedrock provider returns from
// ListFoundationModels, since that's exactly the catalog the translator can
// route to via Converse.
type ModelsHandler struct {
	provider provider.Provider
	logger   *slog.Logger
}

// NewModelsHandler wraps the bedrock provider so its model catalog is
// reachable under the translator's URL space.
func NewModelsHandler(p provider.Provider, logger *slog.Logger) *ModelsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ModelsHandler{provider: p, logger: logger}
}

// ServeHTTP returns the bedrock catalog as an OpenAI list-models response.
// GET-only; HEAD is allowed to keep liveness probes cheap.
func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, `{"error":{"message":"method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
		return
	}

	models, err := h.provider.ListModels(r.Context())
	if err != nil {
		h.logger.Warn("translator models: list failed", "err", err)
		writeJSONError(w, http.StatusBadGateway, "list_models", err.Error())
		return
	}
	if models == nil {
		models = []provider.Model{}
	}

	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   models,
	})
}
