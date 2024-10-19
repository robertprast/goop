package openai

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

type OpenAIEngine struct {
	backends  []*BackendConfig
	whitelist []string
	prefix    string
	logger    *logrus.Entry
}

func NewOpenAIEngineWithConfig(configStr string) (*OpenAIEngine, error) {
	var config map[string]BackendConfig

	err := yaml.Unmarshal([]byte(configStr), &config)
	if err != nil {
		logrus.Fatalf("Error parsing Azure config: %v", err)
	}

	var backends []*BackendConfig
	for _, cfg := range config {
		backends = append(backends, &BackendConfig{
			BackendURL: utils.MustParseURL(cfg.BaseUrl),
			APIKey:     cfg.APIKey,
			APIVersion: cfg.APIVersion,
		})
	}

	if len(backends) == 0 {
		return nil, fmt.Errorf("no backends found in config")
	}

	engine := &OpenAIEngine{
		backends:  backends,
		whitelist: []string{"/v1/chat/completions", "/v1/completions"},
		prefix:    "/openai",
		logger:    logrus.WithField("engine", "openai"),
	}
	return engine, nil
}

func (e *OpenAIEngine) Name() string {
	return "openai"
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
	backend := e.backends[0] // Use the first backend since OpenAI has single API domain
	if backend == nil {
		logrus.Error("No active backends found")
		return
	}

	r.URL.Path = strings.TrimPrefix(r.URL.Path, e.prefix)
	r.Host = backend.BackendURL.Host
	r.URL.Scheme = backend.BackendURL.Scheme
	r.URL.Host = backend.BackendURL.Host

	r.Header.Set("Authorization", "Bearer "+backend.APIKey)
	e.logger.Infof("Modified request for backend: %s", backend.BackendURL)
}

func (e *OpenAIEngine) HandleResponseAfterFinish(resp *http.Response, body []byte) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, len(string(body)))
}
