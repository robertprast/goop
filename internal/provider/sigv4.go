package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// sigV4Transport wraps an inner http.RoundTripper with AWS SigV4 signing.
// Signing requires the SHA256 of the request body, so the body is fully
// buffered on its way upstream. The response stream is NOT buffered.
type sigV4Transport struct {
	creds   aws.CredentialsProvider
	region  string
	service string
	inner   http.RoundTripper
}

// newSigV4Transport returns a RoundTripper that signs with SigV4 for the given
// AWS service (e.g. "bedrock") in the given region.
func newSigV4Transport(creds aws.CredentialsProvider, region, service string, inner http.RoundTripper) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &sigV4Transport{creds: creds, region: region, service: service, inner: inner}
}

func (t *sigV4Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := drainBody(req)
	if err != nil {
		return nil, fmt.Errorf("sigv4: read body: %w", err)
	}

	creds, err := t.creds.Retrieve(req.Context())
	if err != nil {
		return nil, fmt.Errorf("sigv4: retrieve credentials: %w", err)
	}

	stripContaminatedHeaders(req)
	hash := sha256.Sum256(body)
	signer := v4.NewSigner()
	if err := signer.SignHTTP(req.Context(), creds, req, hex.EncodeToString(hash[:]), t.service, t.region, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("sigv4: sign: %w", err)
	}
	return t.inner.RoundTrip(req)
}

// drainBody reads req.Body fully, replaces it with a fresh reader, and returns
// the bytes for hashing. Returns an empty slice when there is no body.
func drainBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	if req.GetBody == nil {
		buf := body
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}
	}
	req.ContentLength = int64(len(body))
	return body, nil
}

// stripContaminatedHeaders drops headers added by the client, intermediaries,
// or the reverse proxy (e.g. Authorization, X-Forwarded-*) that would either
// invalidate the SigV4 signature or be hostile when echoed upstream.
func stripContaminatedHeaders(req *http.Request) {
	clean := http.Header{}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		clean.Set("Content-Type", ct)
	}
	if accept := req.Header.Get("Accept"); accept != "" {
		clean.Set("Accept", accept)
	}
	req.Header = clean
}

// nopCredentialsProvider returns a static-credentials provider for tests.
type nopCredentialsProvider struct{ creds aws.Credentials }

func (n nopCredentialsProvider) Retrieve(_ context.Context) (aws.Credentials, error) {
	return n.creds, nil
}
