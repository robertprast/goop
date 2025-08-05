package gemini

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
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

type BackendConfig struct {
	BackendURL *url.URL
}

var (
	cachedGeminiModels      []openai_schema.Model
	cachedGeminiModelsLock  sync.RWMutex
	cachedGeminiModelsTime  time.Time
	geminiCacheValidityTime = 5 * time.Minute
)

type GeminiEngine struct {
	backends   []*BackendConfig
	prefix     string
	logger     *logrus.Entry
	httpClient *http.Client
}

type geminiModelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
		Created int64  `json:"created"`
	} `json:"data"`
}

func NewGeminiEngine(_ string) (*GeminiEngine, error) {
	u, _ := url.Parse("https://generativelanguage.googleapis.com")
	return &GeminiEngine{
		backends:   []*BackendConfig{{BackendURL: u}},
		prefix:     "/gemini",
		logger:     logrus.WithField("e", "gemini-openai"),
		httpClient: utils.DefaultHTTPClient(),
	}, nil
}

func (e *GeminiEngine) Name() string { return "gemini-openai" }

func (e *GeminiEngine) ListModels() ([]openai_schema.Model, error) {
	// Check cache first
	cachedGeminiModelsLock.RLock()
	if cachedGeminiModels != nil && time.Since(cachedGeminiModelsTime) < geminiCacheValidityTime {
		defer cachedGeminiModelsLock.RUnlock()
		return cachedGeminiModels, nil
	}
	cachedGeminiModelsLock.RUnlock()
	cachedGeminiModelsLock.Lock()
	defer cachedGeminiModelsLock.Unlock()
	if cachedGeminiModels != nil && time.Since(cachedGeminiModelsTime) < geminiCacheValidityTime {
		return cachedGeminiModels, nil
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

	resp, err := e.httpClient.Do(req)
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
		e.logger.Errorf("Gemini returned status code %d, body: %s", resp.StatusCode, string(bodyBytes))
		return nil, fmt.Errorf("gemini returned status code %d", resp.StatusCode)
	}

	var geminiResp geminiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		e.logger.Errorf("failed to decode gemini models response: %v", err)
		return nil, err
	}

	var models []openai_schema.Model
	for _, model := range geminiResp.Data {
		// Remove "models/" prefix if present
		modelID := model.ID
		if strings.HasPrefix(modelID, "models/") {
			modelID = strings.TrimPrefix(modelID, "models/")
		}

		models = append(models, openai_schema.Model{
			ID:      fmt.Sprintf("gemini/%s", modelID),
			Name:    modelID,
			Object:  model.Object,
			Created: model.Created,
			OwnedBy: model.OwnedBy,
		})
	}

	// Update cache
	cachedGeminiModels = models
	cachedGeminiModelsTime = time.Now()

	e.logger.Infof("Found %d models from Gemini API", len(models))
	return models, nil
}

func (e *GeminiEngine) IsAllowedPath(path string) bool {
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

func (e *GeminiEngine) ModifyRequest(r *http.Request) {
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

	path := r.URL.Path
	usingGeminiRawSchema := false
	if strings.HasPrefix(path, e.prefix) {
		e.logger.Infof("Rewriting path for Gemini OpenAI: %s\n", path)
		path = strings.TrimPrefix(path, e.prefix)
		path = strings.Replace(path, "/v1/", "/openai/", 1)
		usingGeminiRawSchema = true
	}

	// Route to Gemini OpenAI layer
	baseURL, err := url.Parse(host)
	if err != nil {
		e.logger.Errorf("Invalid GEMINI_OPENAI_BASE %q: %v", host, err)
		return
	}
	r.URL.Path = path
	r.Host = baseURL.Host
	r.URL.Host = baseURL.Host
	r.URL.Scheme = baseURL.Scheme

	r.Header.Del("X-Goog-Api-Key")
	r.Header.Del("Authorization")
	r.Header.Set("Content-Type", "application/json")
	if usingGeminiRawSchema {
		// For raw Gemini schema, we need to set google headers
		r.Header.Set("X-Goog-Api-Key", apiKey)
	} else {
		// For OpenAI-compatible schema, we use Authorization header
		r.Header.Set("Authorization", "Bearer "+apiKey)
	}
	e.logger.Infof("Gemini OpenAI request → %s", r.URL.String())
}

func (e *GeminiEngine) ResponseCallback(resp *http.Response, body io.Reader) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d",
		resp.StatusCode, id, resp.ContentLength)
}

func (e *GeminiEngine) GetBackendURL() string {
	if len(e.backends) == 0 || e.backends[0].BackendURL == nil {
		return ""
	}
	return e.backends[0].BackendURL.String()
}

func (e *GeminiEngine) GetProjectID() string {
	return "" // not used in Gemini OpenAI mode
}
