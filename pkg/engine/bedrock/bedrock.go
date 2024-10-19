package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/robertprast/goop/pkg/engine"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

type BedrockEngine struct {
	name      string
	backend   *url.URL
	whitelist []string
	prefix    string
	signer    *v4.Signer
	awsConfig aws.Config
}

func NewBedrockEngine() *BedrockEngine {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		logrus.Fatalf("Unable to load AWS SDK config: %v", err)
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	endpoint := "https://bedrock-runtime." + region + ".amazonaws.com"

	engine := &BedrockEngine{
		name:      "bedrock",
		backend:   utils.MustParseURL(endpoint),
		whitelist: []string{"/model/", "/invoke", "/converse", "/converse-stream"},
		prefix:    "/bedrock",
		signer:    v4.NewSigner(),
		awsConfig: cfg,
	}
	return engine
}

func (e *BedrockEngine) Name() string {
	return e.name
}

func (e *BedrockEngine) IsAllowedPath(path string) bool {
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

// Sign the request using AWS SDK v2
func (e *BedrockEngine) signRequest(req *http.Request) {
	creds, err := e.awsConfig.Credentials.Retrieve(context.Background())
	if err != nil {
		logrus.Errorf("Failed to retrieve AWS credentials: %v", err)
		return
	}

	var body []byte
	var payloadHash string
	if req.Body != nil {
		body, err = io.ReadAll(req.Body)
		if err != nil {
			logrus.Errorf("Failed to read request body: %v", err)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		hash := sha256.Sum256(body)
		payloadHash = hex.EncodeToString(hash[:])
	} else {
		// Use SHA-256 hash of an empty string if there is no body
		payloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	}

	// Update the time parsing to match AWS SigV4 format
	signingTime, err := time.Parse("20060102T150405Z", time.Now().UTC().Format("20060102T150405Z"))
	if err != nil {
		logrus.Errorf("Failed to parse signing time: %v", err)
		return
	}

	err = e.signer.SignHTTP(context.Background(), creds, req, payloadHash, "bedrock", e.awsConfig.Region, signingTime)
	if err != nil {
		logrus.Errorf("Failed to sign request: %v", err)
	}
}

func (e *BedrockEngine) HandleResponseAfterFinish(resp *http.Response, body []byte) {
	id, _ := resp.Request.Context().Value(engine.RequestId).(string)
	logrus.Infof("Response [HTTP %d] Correlation ID: %s Body Length: %d\n",
		resp.StatusCode, id, len(string(body)))
}
