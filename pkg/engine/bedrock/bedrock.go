package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/robertprast/goop/pkg/openai_schema"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const DEFAULT_REGION = "us-east-1"
const CR_INFERENCE_PREFIX = "us"

var (
	cachedModels      []openai_schema.Model
	cachedModelsLock  sync.RWMutex
	cachedModelsTime  time.Time
	cacheValidityTime = 5 * time.Minute // Cache valid for 5 minutes
)

type BedrockEngine struct {
	Backend *url.URL
	Client  *bedrockruntime.Client
	Region  string

	whitelist []string
	prefix    string
	awsConfig aws.Config
	signer    *v4.Signer
}

type bedrockConfig struct {
	Enabled bool   `yaml:"enabled"`
	Region  string `yaml:"region"`
}

type foundationModelsResponse struct {
	ModelSummaries []ModelSummary `json:"modelSummaries"`
}

type ModelSummary struct {
	CustomizationsSupported    []string       `json:"customizationsSupported"`
	InferenceTypesSupported    []string       `json:"inferenceTypesSupported"`
	InputModalities            []string       `json:"inputModalities"`
	ModelArn                   string         `json:"modelArn"`
	ModelId                    string         `json:"modelId"`
	ModelLifecycle             ModelLifecycle `json:"modelLifecycle"`
	ModelName                  string         `json:"modelName"`
	OutputModalities           []string       `json:"outputModalities"`
	ProviderName               string         `json:"providerName"`
	ResponseStreamingSupported bool           `json:"responseStreamingSupported"`
}

type ModelLifecycle struct {
	Status string `json:"status"`
}

func NewBedrockEngine(configStr string) (*BedrockEngine, error) {
	var goopConfig bedrockConfig
	err := yaml.Unmarshal([]byte(configStr), &goopConfig)
	if err != nil {
		logrus.Errorf("Unable to unmarshal Bedrock config: %v", err)
		return nil, err
	}
	if !goopConfig.Enabled {
		logrus.Info("Bedrock engine is disabled")
		return nil, fmt.Errorf("engine is disabled")
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		logrus.Errorf("Unable to load AWS SDK config: %v", err)
		return nil, err
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
		Backend:   url,
		whitelist: []string{"/model/", "/invoke", "/converse", "/converse-stream"},
		prefix:    "/bedrock",
		awsConfig: cfg,
		Client:    client,
		signer:    v4.NewSigner(),
		Region:    region,
	}
	return e, nil
}

func (e *BedrockEngine) Name() string {
	return "bedrock"
}

// ListModels reaches out to the AWS Bedrock foundation-models endpoint,
// signs the request, and returns a list of openai_types.Model.
func (e *BedrockEngine) ListModels() ([]openai_schema.Model, error) {
	// Check cache first
	cachedModelsLock.RLock()
	if cachedModels != nil && time.Since(cachedModelsTime) < cacheValidityTime {
		defer cachedModelsLock.RUnlock()
		return cachedModels, nil
	}
	cachedModelsLock.RUnlock()
	cachedModelsLock.Lock()
	defer cachedModelsLock.Unlock()
	if cachedModels != nil && time.Since(cachedModelsTime) < cacheValidityTime {
		return cachedModels, nil
	}

	endpoint := fmt.Sprintf("https://bedrock.%s.amazonaws.com/foundation-models", e.Region)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		logrus.Errorf("failed to create request: %v", err)
		return nil, err
	}
	
	// Sign request manually for bedrock service (not bedrock-runtime)
	creds, err := e.awsConfig.Credentials.Retrieve(context.Background())
	if err != nil {
		logrus.Errorf("Failed to retrieve AWS credentials: %v", err)
		return nil, err
	}
	
	// For GET requests with no body
	payloadHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // empty string SHA256
	err = e.signer.SignHTTP(context.Background(), creds, req, payloadHash, "bedrock", e.Region, time.Now().UTC())
	if err != nil {
		logrus.Errorf("Failed to sign request: %v", err)
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logrus.Errorf("failed to execute request: %v", err)
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logrus.Errorf("failed to close response body: %v", err)
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
		// Skip if streaming is not supported or status is not ACTIVE
		if !summary.ResponseStreamingSupported || summary.ModelLifecycle.Status != "ACTIVE" {
			continue
		}
		// Check for ON_DEMAND inference type
		isOnDemand := false
		for _, inferenceType := range summary.InferenceTypesSupported {
			if inferenceType == "ON_DEMAND" {
				isOnDemand = true
				break
			}
		}
		if isOnDemand {
			models = append(models, openai_schema.Model{
				ID:      fmt.Sprintf("bedrock/%s", summary.ModelId),
				Name:    summary.ModelName,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: summary.ProviderName,
			})
		}
		// Handle cross-region inference models
		if CR_INFERENCE_PREFIX != "" {
			profileID := fmt.Sprintf("%s.%s", CR_INFERENCE_PREFIX, summary.ModelId)
			models = append(models, openai_schema.Model{
				ID:      fmt.Sprintf("bedrock/%s", profileID),
				Name:    summary.ModelName,
				Object:  "model",
				Created: time.Now().Unix(),
				OwnedBy: summary.ProviderName,
			})
		}
	}

	// Update cache
	cachedModels = models
	cachedModelsTime = time.Now()

	logrus.Infof("Found %d filtered models from Bedrock", len(models))
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