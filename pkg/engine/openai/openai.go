package openai

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
)

type BackendConfig struct {
	BackendURL  *url.URL
	APIKey      string
	IsActive    bool
	Connections int64
}

type OpenAIEngine struct {
	name      string
	backends  []*BackendConfig
	whitelist []string
	prefix    string
	logger    *logrus.Entry
}

func NewOpenAIEngine() *OpenAIEngine {
	backends := []*BackendConfig{
		{
			BackendURL:  mustParseURL("https://api.openai.com"),
			APIKey:      "YOUR_API_KEY_1",
			IsActive:    true,
			Connections: 0,
		},
	}
	engine := &OpenAIEngine{
		name:      "openai",
		backends:  backends,
		whitelist: []string{"/v1/chat/completions", "/v1/completions"},
		prefix:    "/openai",
		logger:    logrus.WithField("engine", "openai"),
	}
	return engine
}

func (e *OpenAIEngine) Name() string {
	return e.name
}

func (e *OpenAIEngine) IsValidPath(path string) bool {
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

	atomic.AddInt64(&backend.Connections, 1)
	defer atomic.AddInt64(&backend.Connections, -1)

	r.URL.Path = strings.TrimPrefix(r.URL.Path, e.prefix)
	r.Host = backend.BackendURL.Host
	r.URL.Scheme = backend.BackendURL.Scheme
	r.URL.Host = backend.BackendURL.Host

	r.Header.Set("Authorization", "Bearer "+backend.APIKey)
	e.logger.Infof("Modified request for backend: %s", backend.BackendURL)
}

func mustParseURL(rawURL string) *url.URL {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		panic("Invalid URL: " + rawURL)
	}
	return parsedURL
}

func (e *OpenAIEngine) HandleResponseAfterFinish(resp *http.Response, body []byte) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, len(string(body)))
}
