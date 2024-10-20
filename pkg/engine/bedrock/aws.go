package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
)

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
