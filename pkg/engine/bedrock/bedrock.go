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
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/sirupsen/logrus"
)

type BedrockEngine struct {
	name      string
	backends  []*BackendConfig
	whitelist []string
	prefix    string
	signer    *v4.Signer
	awsConfig aws.Config
}

type BackendConfig struct {
	BackendURL  *url.URL
	IsActive    bool
	Connections int64
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

	backendURL, err := url.Parse(endpoint)
	if err != nil {
		logrus.Fatalf("Invalid Bedrock endpoint URL: %v", err)
	}

	backends := []*BackendConfig{
		{
			BackendURL:  backendURL,
			IsActive:    true,
			Connections: 0,
		},
	}

	if len(backends) == 0 {
		logrus.Fatalf("No backends available")
	}

	engine := &BedrockEngine{
		name:      "bedrock",
		backends:  backends,
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

func (e *BedrockEngine) IsValidPath(path string) bool {
	for _, allowedPath := range e.whitelist {
		if strings.HasPrefix(path, e.prefix+allowedPath) {
			return true
		}
	}
	logrus.Warnf("Path %s is not allowed", path)
	return false
}

func (e *BedrockEngine) ModifyRequest(r *http.Request) {
	backend := e.backends[0]
	if backend == nil {
		logrus.Warn("No backend available")
		return
	}

	atomic.AddInt64(&backend.Connections, 1)
	defer atomic.AddInt64(&backend.Connections, -1)

	r.URL.Path = strings.TrimPrefix(r.URL.Path, e.prefix)
	r.Host = backend.BackendURL.Host
	r.URL.Scheme = backend.BackendURL.Scheme
	r.URL.Host = backend.BackendURL.Host

	r.Header.Del("Authorization")
	r.Header.Del("X-Amz-Content-Sha256")
	r.Header.Del("X-Amz-Security-Token")

	e.signRequest(r)

	logrus.Infof("Modified request for backend: %s", backend.BackendURL)
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
	signingTime, err := time.Parse("20060102T150405Z", req.Header.Get("X-Amz-Date"))
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
	// no-op
}
