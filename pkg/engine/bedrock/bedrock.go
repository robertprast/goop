package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	// imported as openai
)

//var _ engine.OpenAIProxyEngine = (*BedrockEngine)(nil)

type BedrockEngine struct {
	Backend *url.URL
	Client  *bedrockruntime.Client

	whitelist []string
	prefix    string
	awsConfig aws.Config
	signer    *v4.Signer
}

type bedrockConfig struct {
	Enabled bool `yaml:"enabled"`
}

func NewBedrockEngine(configStr string) (*BedrockEngine, error) {
	var goopConfig bedrockConfig

	err := yaml.Unmarshal([]byte(configStr), &goopConfig)
	if err != nil {
		logrus.Errorf("Unable to unmarshal Bedrock config: %v", err)
		return &BedrockEngine{}, err
	}

	if !goopConfig.Enabled {
		logrus.Info("Bedrock engine is disabled")
		return &BedrockEngine{}, fmt.Errorf("engine is disabled")
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		logrus.Errorf("Unable to load AWS SDK config: %v", err)
		return &BedrockEngine{}, err
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	endpoint := "https://bedrock-runtime." + region + ".amazonaws.com"
	url, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	client := bedrockruntime.NewFromConfig(cfg)

	engine := &BedrockEngine{
		Backend:   url,
		whitelist: []string{"/model/", "/invoke", "/converse", "/converse-stream"},
		prefix:    "/bedrock",
		awsConfig: cfg,
		Client:    client,
		signer:    v4.NewSigner(),
	}
	return engine, nil
}

func (e *BedrockEngine) Name() string {
	return "bedrock"
}

func (e *BedrockEngine) IsAllowedPath(path string) bool {
	logrus.Infof("Checking if path %s is allowed", path)
	for _, allowedPath := range e.whitelist {
		if strings.HasPrefix(path, e.prefix+allowedPath) {
			return true
		}
	}
	logrus.Warnf("Path %s is not allowed", path)
	return false
}

func (e *BedrockEngine) ModifyRequest(r *http.Request) {
	r.URL.Path = strings.TrimPrefix(r.URL.Path, e.prefix)
	r.Host = e.Backend.Host
	r.URL.Scheme = e.Backend.Scheme
	r.URL.Host = e.Backend.Host
	r.Header.Del("Authorization")

	e.SignRequest(r)
	logrus.Infof("Modified request for backend: %s", e.Backend)
}

func (e *BedrockEngine) HandleResponseAfterFinish(resp *http.Response, body []byte) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, len(string(body)))
}
