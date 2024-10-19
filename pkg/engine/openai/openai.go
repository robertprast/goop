package openai

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

type BackendConfig struct {
	BackendURL *url.URL
	APIKey     string
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
		// {
		// 	BackendURL:  utils.MustParseURL("https://api.openai.com/v1"),
		// 	APIKey:      os.Getenv("OPENAI_API_KEY"),
		// },
		{
			BackendURL: utils.MustParseURL("http://localhost:1234/v1"),
			APIKey:     "test",
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
