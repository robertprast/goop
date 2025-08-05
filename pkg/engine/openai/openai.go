package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/robertprast/goop/pkg/openai_schema"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type BackendConfig struct {
	BaseUrl    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"`
	APIVersion string `yaml:"api_version"`
	BackendURL *url.URL
}

var (
	cachedOpenAIModels      []openai_schema.Model
	cachedOpenAIModelsLock  sync.RWMutex
	cachedOpenAIModelsTime  time.Time
	openaiCacheValidityTime = 5 * time.Minute
)

type OpenAIEngine struct {
	backend    *BackendConfig
	whitelist  []string
	prefix     string
	logger     *logrus.Entry
	httpClient *http.Client
}

type openaiModelsResponse struct {
	Data []openai_schema.Model `json:"data"`
}

func NewOpenAIEngineWithConfig(configStr string) (*OpenAIEngine, error) {
	var backend BackendConfig

	if err := yaml.Unmarshal([]byte(configStr), &backend); err != nil {
		logrus.Errorf("Error parsing OpenAI config: %v", err)
		return nil, fmt.Errorf("error parsing OpenAI config: %w", err)
	}

	if backend.BaseUrl == "" || backend.APIKey == "" {
		return nil, fmt.Errorf("error parsing OpenAI config: missing base_url or api_key")
	}

	parsedUrl, err := url.Parse(backend.BaseUrl)
	if err != nil {
		return nil, err
	}
	backend.BackendURL = parsedUrl

	e := &OpenAIEngine{
		backend:    &backend,
		whitelist:  []string{"/v1/chat/completions", "/v1/completions", "/v1/models", "/v1/embeddings", "/v1/responses"},
		prefix:     "/openai",
		logger:     logrus.WithField("e", "openai"),
		httpClient: utils.DefaultHTTPClient(),
	}
	return e, nil
}

func (e *OpenAIEngine) Name() string {
	return "openai"
}

func (e *OpenAIEngine) ListModels() ([]openai_schema.Model, error) {
	// Check cache first
	cachedOpenAIModelsLock.RLock()
	if cachedOpenAIModels != nil && time.Since(cachedOpenAIModelsTime) < openaiCacheValidityTime {
		defer cachedOpenAIModelsLock.RUnlock()
		return cachedOpenAIModels, nil
	}
	cachedOpenAIModelsLock.RUnlock()
	cachedOpenAIModelsLock.Lock()
	defer cachedOpenAIModelsLock.Unlock()
	if cachedOpenAIModels != nil && time.Since(cachedOpenAIModelsTime) < openaiCacheValidityTime {
		return cachedOpenAIModels, nil
	}

	baseURL := strings.TrimSuffix(e.backend.BackendURL.String(), "/")
	endpoint := fmt.Sprintf("%s/v1/models", baseURL)
	// If base URL already includes /v1, don't double it
	if strings.HasSuffix(baseURL, "/v1") {
		endpoint = fmt.Sprintf("%s/models", baseURL)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		e.logger.Errorf("failed to create request: %v", err)
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.backend.APIKey)

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
		e.logger.Errorf("OpenAI returned status code %d, body: %s", resp.StatusCode, string(bodyBytes))
		return nil, fmt.Errorf("openai returned status code %d", resp.StatusCode)
	}

	var openaiResp openaiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		e.logger.Errorf("failed to decode openai models response: %v", err)
		return nil, err
	}

	var models []openai_schema.Model
	for _, model := range openaiResp.Data {
		models = append(models, openai_schema.Model{
			ID:      fmt.Sprintf("openai/%s", model.ID),
			Name:    model.ID,
			Object:  model.Object,
			Created: model.Created,
			OwnedBy: model.OwnedBy,
		})
	}

	// Update cache
	cachedOpenAIModels = models
	cachedOpenAIModelsTime = time.Now()

	e.logger.Infof("Found %d models from OpenAI", len(models))
	return models, nil
}

func (e *OpenAIEngine) IsAllowedPath(path string) bool {
	for _, allowedPath := range e.whitelist {
		if strings.HasPrefix(path, e.prefix+allowedPath) {
			return true
		}
	}
	e.logger.Warnf("Path %s is not allowed", path)
	return false
}

func (e *OpenAIEngine) ModifyRequest(r *http.Request) {
	r.URL.Path = strings.TrimPrefix(r.URL.Path, e.prefix)
	r.Host = e.backend.BackendURL.Host
	r.URL.Scheme = e.backend.BackendURL.Scheme
	r.URL.Host = e.backend.BackendURL.Host

	r.Header.Set("Authorization", "Bearer "+e.backend.APIKey)
	e.logger.Infof("Modified request for backend: %s", e.backend.BackendURL)
}

func (e *OpenAIEngine) ResponseCallback(resp *http.Response, body io.Reader) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, resp.ContentLength)
}
