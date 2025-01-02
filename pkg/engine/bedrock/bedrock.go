package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/robertprast/goop/pkg/openai_schema"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	// imported as openai_schema
)

const DEFAULT_REGION = "us-east-1"

type globalModels []struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

type BedrockEngine struct {
	Backend *url.URL
	Client  *bedrockruntime.Client
	Region  string

	whitelist    []string
	globalModels globalModels
	prefix       string
	awsConfig    aws.Config
	signer       *v4.Signer
}

type bedrockConfig struct {
	Enabled      bool         `yaml:"enabled"`
	Region       string       `yaml:"region"`
	GlobalModels globalModels `yaml:"global_models"`
}

func NewBedrockEngine(configStr string) (*BedrockEngine, error) {
	var goopConfig bedrockConfig
	err := yaml.Unmarshal([]byte(configStr), &goopConfig)
	if err != nil {
		logrus.Errorf("Unable to unmarshal Bedrock config: %v", err)
		return &BedrockEngine{}, err
	}
	if !goopConfig.Enabled {
		logrus.Info("Bedrock e is disabled")
		return &BedrockEngine{}, fmt.Errorf("e is disabled")
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		logrus.Errorf("Unable to load AWS SDK config: %v", err)
		return &BedrockEngine{}, err
	}
	var region string
	if goopConfig.Region == "" {
		if cfg.Region != "" {
			region = cfg.Region
		} else {
			region = DEFAULT_REGION
		}
	} else {
		region = goopConfig.Region
	}

	endpoint := "https://bedrock-runtime." + region + ".amazonaws.com"
	url, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	client := bedrockruntime.NewFromConfig(cfg)

	e := &BedrockEngine{
		Backend:      url,
		whitelist:    []string{"/model/", "/invoke", "/converse", "/converse-stream"},
		prefix:       "/bedrock",
		awsConfig:    cfg,
		Client:       client,
		signer:       v4.NewSigner(),
		Region:       region,
		globalModels: goopConfig.GlobalModels,
	}
	return e, nil
}

func (e *BedrockEngine) Name() string {
	return "bedrock"
}

// foundationModelsResponse matches the JSON from calling GET /foundation-models
// based on the example response you shared.
type foundationModelsResponse struct {
	ModelSummaries []struct {
		CustomizationsSupported []string `json:"customizationsSupported"`
		InferenceTypesSupported []string `json:"inferenceTypesSupported"`
		InputModalities         []string `json:"inputModalities"`
		ModelArn                string   `json:"modelArn"`
		ModelId                 string   `json:"modelId"`
		ModelLifecycle          struct {
			Status string `json:"status"`
		} `json:"modelLifecycle"`
		ModelName                string   `json:"modelName"`
		OutputModalities         []string `json:"outputModalities"`
		ProviderName             string   `json:"providerName"`
		ResponseStreamingSupport bool     `json:"responseStreamingSupported"`
	} `json:"modelSummaries"`
}

// ListModels reaches out to the AWS Bedrock foundation-models endpoint,
// signs the request, and returns a list of openai_types.Model.
func (e *BedrockEngine) ListModels() ([]openai_schema.Model, error) {
	endpoint := fmt.Sprintf("https://bedrock.%s.amazonaws.com/foundation-models", e.Region)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		logrus.Errorf("failed to create request: %v", err)
		return nil, err
	}
	e.SignRequest(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logrus.Errorf("failed to execute request: %v", err)
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logrus.Errorf("Bedrock returned status code %d, body: %s", resp.StatusCode, string(bodyBytes))
		return nil, fmt.Errorf("bedrock returned status code %d", resp.StatusCode)
	}

	var fmResp foundationModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&fmResp); err != nil {
		logrus.Errorf("failed to decode foundation-models response: %v", err)
		return nil, err
	}

	var models []openai_schema.Model
	for _, summary := range fmResp.ModelSummaries {
		models = append(models, openai_schema.Model{
			ID:      fmt.Sprintf("bedrock/%s", summary.ModelId),
			Name:    summary.ModelName,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: summary.ProviderName,
		})
	}

	for _, summary := range e.globalModels {
		models = append(models, openai_schema.Model{
			ID:      fmt.Sprintf("bedrock/%s", summary.ID),
			Name:    summary.Name,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "AWS",
		})
	}

	logrus.Infof("Found %d models from Bedrock", len(models))
	return models, nil
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

func (e *BedrockEngine) ResponseCallback(resp *http.Response, body io.Reader) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, resp.ContentLength)
}
