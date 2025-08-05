package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

// SignRequest signs an HTTP request for AWS Bedrock in-place.
// This function is designed for proxy environments like AWS Lambda, where the incoming
// request must be "cleaned" of extra headers before signing to avoid signature mismatches.
func (e *BedrockEngine) SignRequest(req *http.Request) {
	// 1. Read and buffer the body. This is necessary for calculating the payload hash.
	// We then replace req.Body so it can be read again by the HTTP client.
	var body []byte
	var err error
	
	if req.Body != nil {
		body, err = io.ReadAll(req.Body)
		if err != nil {
			logrus.Errorf("Failed to read request body for signing: %v", err)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	} else {
		// Handle nil body case (e.g., GET requests)
		body = []byte{}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}

	// 2. Overwrite the request URL and Host to point to the real Bedrock endpoint.
	// The original req.URL.Path from the client is preserved.
	targetHost := fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", e.Region)
	req.URL.Scheme = "https"
	req.URL.Host = targetHost
	req.Host = targetHost // The 'Host' header is critical for a valid signature.

	// 3. Clean the request headers. This is the most important step.
	// We create a new header map, copy only the essential headers, and then replace
	// the original headers. This removes contaminating headers from API Gateway/Lambda.
	cleanHeaders := make(http.Header)
	if contentType := req.Header.Get("Content-Type"); contentType != "" {
		cleanHeaders.Set("Content-Type", contentType)
	}
	req.Header = cleanHeaders

	// 4. Retrieve AWS credentials from your configuration.
	creds, err := e.awsConfig.Credentials.Retrieve(context.Background())
	if err != nil {
		logrus.Errorf("Failed to retrieve AWS credentials: %v", err)
		return
	}

	// 5. Calculate the SHA256 hash of the request body payload.
	hash := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(hash[:])

	// 6. Sign the now-modified request in-place using the SigV4 signer.
	// The signer will add the 'Authorization', 'X-Amz-Date', etc., headers.
	err = e.signer.SignHTTP(context.Background(), creds, req, payloadHash, "bedrock", e.Region, time.Now().UTC())
	if err != nil {
		logrus.Errorf("Failed to sign request: %v", err)
	}
}
