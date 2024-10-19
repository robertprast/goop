package azure

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const (
	DefaultFallthroughModel = "gpt-4o-mini"
)

type BackendConfig struct {
	BaseUrl     string `yaml:"base_url"`
	APIKey      string `yaml:"api_key"`
	APIVersion  string `yaml:"api_version"`
	BackendURL  *url.URL
	IsActive    bool
	Connections int64
}

type AzureOpenAIEngine struct {
	backends  []*BackendConfig
	whitelist map[string]struct{}
	prefix    string
	logger    *logrus.Entry
}

func NewAzureOpenAIEngineWithConfig(configStr string) (*AzureOpenAIEngine, error) {
	var config []BackendConfig
	err := yaml.Unmarshal([]byte(configStr), &config)
	if err != nil {
		logrus.Warnf("Error parsing Azure config: %v", err)
		return &AzureOpenAIEngine{}, fmt.Errorf("error parsing Azure config: %v", err)
	}

	var backends []*BackendConfig
	for _, cfg := range config {
		url, err := url.Parse(cfg.BaseUrl)
		if err != nil {
			return nil, err
		}

		backends = append(backends, &BackendConfig{
			BackendURL:  url,
			APIKey:      cfg.APIKey,
			APIVersion:  cfg.APIVersion,
			IsActive:    true,
			Connections: 0,
		})
	}

	if len(backends) == 0 {
		return &AzureOpenAIEngine{}, fmt.Errorf("no backends found in config")
	}

	logrus.Infof("Backends: %v", backends)

	whitelist := map[string]struct{}{
		"chat/completions": {},
		"completions":      {},
	}
	engine := &AzureOpenAIEngine{
		backends:  backends,
		whitelist: whitelist,
		prefix:    "/azure",
		logger:    logrus.WithField("engine", "azure"),
	}
	engine.startHealthCheck()
	return engine, nil
}

func (e *AzureOpenAIEngine) Name() string {
	return "azure"
}

func (e *AzureOpenAIEngine) IsAllowedPath(path string) bool {
	trimmedPath := strings.TrimPrefix(path, e.prefix)
	deploymentRoute := extractDeploymentRoute(trimmedPath)
	e.logger.Infof("Deployment Route: %s", deploymentRoute)
	_, isAllowed := e.whitelist[deploymentRoute]
	if isAllowed {
		return true
	}
	e.logger.Warnf("Path %s is not allowed", path)
	return false
}

func (e *AzureOpenAIEngine) ModifyRequest(r *http.Request) {
	backend, err := e.selectLeastLoadedBackend()
	if err != nil {
		e.logger.Error("No active backends found")
		return
	}

	atomic.AddInt64(&backend.Connections, 1)
	defer atomic.AddInt64(&backend.Connections, -1)

	r.URL.Path = strings.Replace(r.URL.Path, "/azure", "/openai", 1)

	r.Host = backend.BackendURL.Host
	r.URL.Scheme = backend.BackendURL.Scheme
	r.URL.Host = backend.BackendURL.Host

	logrus.Infof("URL : %s", r.URL.String())

	r.Header.Set("Authorization", "Bearer "+backend.APIKey)

	query := r.URL.Query()
	query.Set("api-version", backend.APIVersion)
	r.URL.RawQuery = query.Encode()

	e.logger.Infof("Modified request for backend: %s", backend.BackendURL)
}

func (e *AzureOpenAIEngine) HandleResponseAfterFinish(resp *http.Response, body []byte) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	e.logger.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, len(body))
}

func extractDeploymentRoute(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 4 {
		return strings.Join(parts[4:], "/")
	}
	return ""
}
