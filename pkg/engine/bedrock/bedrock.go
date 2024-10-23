package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/robertprast/goop/pkg/engine"
	openai_types "github.com/robertprast/goop/pkg/openai_proxy/types"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	// imported as openai
)

var _ engine.OpenAIProxyEngine = (*BedrockEngine)(nil)

type BedrockEngine struct {
	backend   *url.URL
	whitelist []string
	prefix    string
	awsConfig aws.Config
	Client    *bedrockruntime.Client
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
		backend:   url,
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
	r.Host = e.backend.Host
	r.URL.Scheme = e.backend.Scheme
	r.URL.Host = e.backend.Host
	r.Header.Del("Authorization")

	e.signRequest(r)
	logrus.Infof("Modified request for backend: %s", e.backend)
}

func (e *BedrockEngine) TransformChatCompletionRequest(reqBody openai_types.InconcomingChatCompletionRequest) ([]byte, error) {

	logrus.Infof("Request params: %v", reqBody)

	// log the requbody as a pretty json string for debugging
	reqBodyStr, _ := json.MarshalIndent(reqBody, "", "  ")
	logrus.Infof("Request body: %s", reqBodyStr)

	bedrockRequest := BedrockRequest{
		Messages:        transformMessages(reqBody.Messages),
		InferenceConfig: buildInferenceConfig(reqBody),
		System: []SystemMessage{
			{Text: "You are an assistant."},
		},
	}

	toolConfig := buildToolConfig(reqBody)
	if toolConfig != nil && len(toolConfig.Tools) > 0 {
		bedrockRequest.ToolConfig = toolConfig
	}

	// log the bedrock request as a pretty json string for debugging
	bedrockRequestStr, _ := json.MarshalIndent(bedrockRequest, "", "  ")
	logrus.Infof("Bedrock request: %s", bedrockRequestStr)

	return json.Marshal(bedrockRequest)
}

func (e *BedrockEngine) HandleChatCompletionRequest(ctx context.Context, transformedBody []byte, stream bool) (*http.Response, error) {

	endpoint := fmt.Sprintf("%s/model/%s/%s", e.backend.String(), "us.anthropic.claude-3-haiku-20240307-v1:0", getEndpointSuffix(stream))

	logrus.Infof("Request body: %s", transformedBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(transformedBody))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	e.signRequest(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making HTTP request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logrus.Errorf("Bedrock API error: Status %d, Body: %s", resp.StatusCode, string(body))
		resp.Body = io.NopCloser(bytes.NewBuffer(body))
	}

	return resp, nil
}

func (e *BedrockEngine) SendChatCompletionResponse(bedrockResp *http.Response, w http.ResponseWriter, stream bool) error {
	logrus.Infof("Sending request to bedrock")
	if bedrockResp.Header.Get("Content-Type") == "application/vnd.amazon.eventstream" {
		return e.handleStreamingResponse(bedrockResp, w)
	}
	return e.handleNonStreamingResponse(bedrockResp, w)
}

func (e *BedrockEngine) HandleResponseAfterFinish(resp *http.Response, body []byte) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, len(string(body)))
}

func getEndpointSuffix(stream bool) string {
	if stream {
		return "converse-stream"
	}
	return "converse"
}
