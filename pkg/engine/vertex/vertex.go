package vertex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/openai_schema"
	"github.com/sirupsen/logrus"
)

type BackendConfig struct {
	BackendURL *url.URL
}

var (
	cachedVertexModels      []openai_schema.Model
	cachedVertexModelsLock  sync.RWMutex
	cachedVertexModelsTime  time.Time
	vertexCacheValidityTime = 5 * time.Minute
)

type VertexEngine struct {
	backends []*BackendConfig
	prefix   string
	logger   *logrus.Entry
}

type vertexModelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
		Created int64  `json:"created"`
	} `json:"data"`
}

func NewVertexEngine(_ string) (*VertexEngine, error) {
	// Keep type/name to avoid changing other parts of your app.
	u, _ := url.Parse("https://generativelanguage.googleapis.com")
	return &VertexEngine{
		backends: []*BackendConfig{{BackendURL: u}},
		prefix:   "/vertex",
		logger:   logrus.WithField("e", "gemini-openai"),
	}, nil
}

func (e *VertexEngine) Name() string { return "gemini-openai" }

func (e *VertexEngine) ListModels() ([]openai_schema.Model, error) {
	// Check cache first
	cachedVertexModelsLock.RLock()
	if cachedVertexModels != nil && time.Since(cachedVertexModelsTime) < vertexCacheValidityTime {
		defer cachedVertexModelsLock.RUnlock()
		return cachedVertexModels, nil
	}
	cachedVertexModelsLock.RUnlock()
	cachedVertexModelsLock.Lock()
	defer cachedVertexModelsLock.Unlock()
	if cachedVertexModels != nil && time.Since(cachedVertexModelsTime) < vertexCacheValidityTime {
		return cachedVertexModels, nil
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		e.logger.Error("GEMINI_API_KEY must be set")
		return nil, fmt.Errorf("GEMINI_API_KEY must be set")
	}

	endpoint := "https://generativelanguage.googleapis.com/v1beta/openai/models"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		e.logger.Errorf("failed to create request: %v", err)
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.logger.Errorf("failed to execute request: %v", err)
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			e.logger.Errorf("failed to close response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		e.logger.Errorf("Vertex returned status code %d, body: %s", resp.StatusCode, string(bodyBytes))
		return nil, fmt.Errorf("vertex returned status code %d", resp.StatusCode)
	}

	var vertexResp vertexModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&vertexResp); err != nil {
		e.logger.Errorf("failed to decode vertex models response: %v", err)
		return nil, err
	}

	var models []openai_schema.Model
	for _, model := range vertexResp.Data {
		// Remove "models/" prefix if present
		modelID := model.ID
		if strings.HasPrefix(modelID, "models/") {
			modelID = strings.TrimPrefix(modelID, "models/")
		}
		
		models = append(models, openai_schema.Model{
			ID:      fmt.Sprintf("vertex/%s", modelID),
			Name:    modelID,
			Object:  model.Object,
			Created: model.Created,
			OwnedBy: model.OwnedBy,
		})
	}

	// Update cache
	cachedVertexModels = models
	cachedVertexModelsTime = time.Now()

	e.logger.Infof("Found %d models from Vertex AI", len(models))
	return models, nil
}

func (e *VertexEngine) IsAllowedPath(path string) bool {
	trimmed := strings.TrimPrefix(path, e.prefix)
	if strings.Contains(trimmed, "/chat/completions") ||
		strings.Contains(trimmed, "/responses") ||
		strings.Contains(trimmed, "/embeddings") ||
		strings.Contains(trimmed, "/models") {
		return true
	}
	e.logger.Warnf("Path %s is not allowed (Gemini OpenAI-only)", path)
	return false
}

func (e *VertexEngine) ModifyRequest(r *http.Request) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		e.logger.Error("GEMINI_API_KEY must be set")
		return
	}
	host := os.Getenv("GEMINI_OPENAI_BASE")
	if host == "" {
		host = "https://generativelanguage.googleapis.com"
	}
	version := os.Getenv("GEMINI_OPENAI_VERSION")
	if version == "" {
		version = "v1beta"
	}

	original := r.URL.Path
	trimmed := strings.TrimPrefix(original, e.prefix)

	// Accept a few ingress forms and normalize to /{version}/openai/<suffix>
	suffix := strings.TrimPrefix(trimmed, "/openai-proxy/v1/")
	if suffix == trimmed {
		suffix = strings.TrimPrefix(trimmed, "/v1/")
	}
	if suffix == trimmed {
		// If nothing matched, try from the absolute path (supports calling /v1/... directly)
		suffix = strings.TrimPrefix(original, "/openai-proxy/v1/")
		suffix = strings.TrimPrefix(suffix, "/v1/")
	}

	// Route to Gemini OpenAI layer
	baseURL, err := url.Parse(host)
	if err != nil {
		e.logger.Errorf("Invalid GEMINI_OPENAI_BASE %q: %v", host, err)
		return
	}
	r.URL.Path = "/" + version + "/openai/" + strings.TrimPrefix(suffix, "/")
	r.Host = baseURL.Host
	r.URL.Host = baseURL.Host
	r.URL.Scheme = baseURL.Scheme

	// Auth: Bearer <GEMINI_API_KEY>, not X-Goog-Api-Key for this OpenAI layer.
	r.Header.Del("X-Goog-Api-Key")
	r.Header.Set("Authorization", "Bearer "+apiKey)

	e.logger.Infof("Gemini OpenAI request → %s", r.URL.String())
}

func (e *VertexEngine) ResponseCallback(resp *http.Response, body io.Reader) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d",
		resp.StatusCode, id, resp.ContentLength)
}

func (e *VertexEngine) GetBackendURL() string {
	if len(e.backends) == 0 || e.backends[0].BackendURL == nil {
		return ""
	}
	return e.backends[0].BackendURL.String()
}

func (e *VertexEngine) GetProjectID() string {
	return "" // not used in Gemini OpenAI mode
}
