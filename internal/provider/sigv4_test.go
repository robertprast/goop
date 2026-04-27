package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// captureRT records the request it sees and returns a canned response.
type captureRT struct {
	got     *http.Request
	gotBody []byte
	resp    *http.Response
	err     error
}

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.got = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		c.gotBody = b
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
	}, nil
}

func TestDrainBody_ReplacesBodyAndSetsGetBody(t *testing.T) {
	original := []byte(`{"hello":"world"}`)
	req, err := http.NewRequest(http.MethodPost, "https://example.com", bytes.NewReader(original))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.GetBody = nil // ensure drainBody sets it

	got, err := drainBody(req)
	if err != nil {
		t.Fatalf("drainBody: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("returned bytes mismatch: got %q want %q", got, original)
	}
	if req.ContentLength != int64(len(original)) {
		t.Errorf("ContentLength: got %d want %d", req.ContentLength, len(original))
	}
	// Read the replaced body — should match original.
	replaced, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read replaced body: %v", err)
	}
	if !bytes.Equal(replaced, original) {
		t.Errorf("replaced body mismatch: got %q want %q", replaced, original)
	}
	// GetBody must have been set, and must yield identical bytes.
	if req.GetBody == nil {
		t.Fatal("GetBody is nil after drainBody")
	}
	rc, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody returned error: %v", err)
	}
	defer rc.Close()
	gotGB, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read GetBody: %v", err)
	}
	if !bytes.Equal(gotGB, original) {
		t.Errorf("GetBody bytes mismatch: got %q want %q", gotGB, original)
	}
	// Calling GetBody again should also return identical bytes (independent copy).
	rc2, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody second call: %v", err)
	}
	defer rc2.Close()
	gotGB2, _ := io.ReadAll(rc2)
	if !bytes.Equal(gotGB2, original) {
		t.Errorf("GetBody second call bytes mismatch: got %q want %q", gotGB2, original)
	}
}

func TestDrainBody_NilBodyReturnsNil(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	got, err := drainBody(req)
	if err != nil {
		t.Fatalf("drainBody: %v", err)
	}
	if got != nil {
		t.Errorf("want nil bytes, got %v", got)
	}
}

func TestDrainBody_NoBodySentinel(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Body = http.NoBody
	got, err := drainBody(req)
	if err != nil {
		t.Fatalf("drainBody: %v", err)
	}
	if got != nil {
		t.Errorf("want nil bytes for http.NoBody, got %v", got)
	}
}

func TestStripContaminatedHeaders_Whitelist(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("Custom-Header", "x")
	req.Header.Set("X-Api-Key", "secret")

	stripContaminatedHeaders(req)

	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type should be preserved: got %q", got)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept should be preserved: got %q", got)
	}
	for _, k := range []string{"Authorization", "X-Forwarded-For", "Custom-Header", "X-Api-Key"} {
		if got := req.Header.Get(k); got != "" {
			t.Errorf("%s should be stripped, got %q", k, got)
		}
	}
}

func TestStripContaminatedHeaders_NoSourceHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// no headers at all - should not panic and result in empty header map
	stripContaminatedHeaders(req)
	if len(req.Header) != 0 {
		t.Errorf("expected empty header map, got %v", req.Header)
	}
}

func TestSigV4Transport_RoundTripSignsAndForwards(t *testing.T) {
	body := []byte(`{"prompt":"hello"}`)
	req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/abc/converse", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-token") // should be stripped before signing

	creds := nopCredentialsProvider{creds: aws.Credentials{
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "secret",
	}}
	cap := &captureRT{}
	tr := newSigV4Transport(creds, "us-east-1", "bedrock", cap)

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if cap.got == nil {
		t.Fatal("inner transport never received the request")
	}

	// Body bytes should reach the inner transport intact (drainBody replaces the body).
	if !bytes.Equal(cap.gotBody, body) {
		t.Errorf("inner body: got %q want %q", cap.gotBody, body)
	}

	// X-Amz-Date must be set after signing.
	if d := cap.got.Header.Get("X-Amz-Date"); d == "" {
		t.Error("X-Amz-Date not set")
	}

	// Authorization must be replaced with AWS4-HMAC-SHA256 prefix.
	auth := cap.got.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization not signed: %q", auth)
	}

	// Signed payload hash should match SHA256(body). The signer puts the hash
	// header in X-Amz-Content-Sha256 if it sets it; check the credential scope
	// in Authorization for service+region.
	hash := sha256.Sum256(body)
	hexHash := hex.EncodeToString(hash[:])
	if x := cap.got.Header.Get("X-Amz-Content-Sha256"); x != "" && x != hexHash {
		t.Errorf("X-Amz-Content-Sha256: got %q want %q", x, hexHash)
	}
	if !strings.Contains(auth, "us-east-1/bedrock/aws4_request") {
		t.Errorf("Authorization missing scope: %q", auth)
	}

	// Contaminated headers should not have been signed (they are stripped first).
	// The simplest check: original Authorization "Bearer client-token" must be gone.
	if strings.Contains(auth, "Bearer") {
		t.Error("client Authorization leaked into upstream auth header")
	}
}

func TestSigV4Transport_CredentialsErrorBubblesUp(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/x", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	cap := &captureRT{}
	tr := newSigV4Transport(errCreds{err: errors.New("boom")}, "us-east-1", "bedrock", cap)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("want error from credential failure, got nil")
	}
	if cap.got != nil {
		t.Error("inner transport should not have been called on cred failure")
	}
}

func TestSigV4Transport_NilInnerUsesDefault(t *testing.T) {
	// The newSigV4Transport contract: nil inner becomes http.DefaultTransport.
	// We can't easily exercise DefaultTransport without a network, but we can
	// at least verify the constructor accepts nil and returns a non-nil RT.
	creds := nopCredentialsProvider{creds: aws.Credentials{AccessKeyID: "A", SecretAccessKey: "B"}}
	tr := newSigV4Transport(creds, "us-east-1", "bedrock", nil)
	if tr == nil {
		t.Fatal("expected non-nil RoundTripper")
	}
}

type errCreds struct{ err error }

func (e errCreds) Retrieve(_ context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, e.err
}
