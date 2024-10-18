package azure

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
)

const (
	DefaultFallthroughModel = "gpt-4o-mini"
)

type BackendConfig struct {
	BackendURL  *url.URL
	APIKey      string
	APIVersion  string
	IsActive    bool
	Connections int64
}

type AzureOpenAIEngine struct {
	name      string
	backends  []*BackendConfig
	whitelist map[string]struct{}
	prefix    string
	logger    *logrus.Entry
}

func NewAzureOpenAIEngine() *AzureOpenAIEngine {
	backends := []*BackendConfig{
		{
			BackendURL:  mustParseURL("https://test.azure.com"),
			APIKey:      "1234",
			APIVersion:  "2024-04-01-preview",
			IsActive:    true,
			Connections: 0,
		},
	}
	whitelist := map[string]struct{}{
		"chat/completions": {},
		"completions":      {},
	}
	engine := &AzureOpenAIEngine{
		name:      "azure",
		backends:  backends,
		whitelist: whitelist,
		prefix:    "/azure",
		logger:    logrus.WithField("engine", "azure"),
	}
	engine.startHealthCheck()
	return engine
}

func (e *AzureOpenAIEngine) Name() string {
	return e.name
}

func (e *AzureOpenAIEngine) IsValidPath(path string) bool {
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
	backend := e.selectLeastLoadedBackend()
	if backend == nil {
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

	r.Header.Set("api-key", backend.APIKey)

	query := r.URL.Query()
	query.Set("api-version", backend.APIVersion)
	r.URL.RawQuery = query.Encode()

	e.logger.Infof("Modified request for backend: %s", backend.BackendURL)
}

func mustParseURL(rawURL string) *url.URL {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		panic("Invalid URL: " + rawURL)
	}
	return parsedURL
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
