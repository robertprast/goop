package vertex

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2/google"
)

type BackendConfig struct {
	BackendURL *url.URL
}

type VertexEngine struct {
	name     string
	backends []*BackendConfig
	prefix   string
	logger   *logrus.Entry
}

func NewVertexEngine() *VertexEngine {
	backends := []*BackendConfig{
		{
			BackendURL: utils.MustParseURL("https://us-central1-aiplatform.googleapis.com"),
		},
	}
	engine := &VertexEngine{
		name:     "vertex",
		backends: backends,
		prefix:   "/vertex",
		logger:   logrus.WithField("engine", "vertex"),
	}
	return engine
}

func (e *VertexEngine) Name() string {
	return e.name
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

	r.URL.Path = strings.TrimPrefix(r.URL.Path, e.prefix)
	r.Host = backend.BackendURL.Host
	r.URL.Scheme = backend.BackendURL.Scheme
	r.URL.Host = backend.BackendURL.Host

	token, err := getAccessToken()
	if err != nil {
		e.logger.Errorf("Failed to obtain access token: %v", err)
		return
	}
	r.Header.Set("Authorization", "Bearer "+token)

	e.logger.Infof("Modified request URL: %s", r.URL.String())
}

func (e *VertexEngine) HandleResponseAfterFinish(resp *http.Response, body []byte) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	e.logger.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, len(body))
}

func getAccessToken() (string, error) {
	ctx := context.Background()
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", err
	}
	tokenSource := creds.TokenSource
	token, err := tokenSource.Token()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}
