package openai

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/robertprast/goop/pkg/openai_schema"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type BackendConfig struct {
	BaseUrl    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"`
	APIVersion string `yaml:"api_version"`
	BackendURL *url.URL
}

type OpenAIEngine struct {
	backend   *BackendConfig
	whitelist []string
	prefix    string
	logger    *logrus.Entry
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
		backend:   &backend,
		whitelist: []string{"/v1/chat/completions", "/v1/completions", "/v1/models", "/v1/embeddings", "/v1/responses"},
		prefix:    "/openai",
		logger:    logrus.WithField("e", "openai"),
	}
	return e, nil
}

func (e *OpenAIEngine) Name() string {
	return "openai"
}

func (e *OpenAIEngine) ListModels() ([]openai_schema.Model, error) {
	return []openai_schema.Model{}, nil
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
