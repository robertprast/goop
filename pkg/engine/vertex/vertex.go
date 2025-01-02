package vertex

import (
	"context"
	"fmt"
	"github.com/robertprast/goop/pkg/openai_schema"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2/google"
	"gopkg.in/yaml.v2"
)

type BackendConfig struct {
	BackendURL *url.URL
}

type VertexEngine struct {
	backends []*BackendConfig
	prefix   string
	logger   *logrus.Entry
}

type vertexConfig struct {
	Enabled bool `yaml:"enabled"`
}

func NewVertexEngine(configStr string) (*VertexEngine, error) {
	var goopConfig vertexConfig

	err := yaml.Unmarshal([]byte(configStr), &goopConfig)
	if err != nil {
		logrus.Errorf("Error parsing Vertex config: %v", err)
		return &VertexEngine{}, fmt.Errorf("error parsing Vertex config: %w", err)
	}

	if !goopConfig.Enabled {
		logrus.Info("Bedrock e is disabled")
		return &VertexEngine{}, err
	}

	url, err := url.Parse("https://us-central1-aiplatform.googleapis.com")
	if err != nil {
		return nil, err
	}

	e := &VertexEngine{
		backends: []*BackendConfig{
			{
				BackendURL: url,
			}},
		prefix: "/vertex",
		logger: logrus.WithField("e", "vertex"),
	}
	return e, nil
}

func (e *VertexEngine) Name() string {
	return "vertex"
}

func (e *VertexEngine) ListModels() ([]openai_schema.Model, error) {
	return []openai_schema.Model{}, nil
}

func (e *VertexEngine) IsAllowedPath(path string) bool {
	trimmedPath := strings.TrimPrefix(path, e.prefix)
	if strings.HasPrefix(trimmedPath, "/v1/projects/") || strings.HasPrefix(trimmedPath, "/v1beta1/projects/") {
		return true
	}
	e.logger.Warnf("Path %s is not allowed", path)
	return false
}

func (e *VertexEngine) ModifyRequest(r *http.Request) {
	backend := e.backends[0] // Use the first backend TODO: add global regions support
	logrus.Infof("%#v", backend)

	r.URL.Path = strings.TrimPrefix(r.URL.Path, e.prefix)
	r.Host = backend.BackendURL.Host
	r.URL.Host = backend.BackendURL.Host
	r.URL.Scheme = backend.BackendURL.Scheme

	token, err := getAccessToken()
	if err != nil {
		e.logger.Errorf("Failed to obtain access token: %v", err)
		return
	}
	r.Header.Set("Authorization", "Bearer "+token)

	e.logger.Infof("Modified request URL: %s", r.URL.String())
}

func (e *VertexEngine) ResponseCallback(resp *http.Response, body io.Reader) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, resp.ContentLength)
}

func getAccessToken() (string, error) {
	ctx := context.Background()
	engine, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", err
	}
	tokenSource := engine.TokenSource
	token, err := tokenSource.Token()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}
