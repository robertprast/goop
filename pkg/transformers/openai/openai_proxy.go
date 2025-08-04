package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robertprast/goop/pkg/engine/openai"
	"github.com/robertprast/goop/pkg/openai_schema"
	"github.com/sirupsen/logrus"
)

// flushWriter wraps an http.ResponseWriter and flushes after every write for immediate streaming
type flushWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (fw *flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.writer.Write(p)
	if err == nil {
		fw.flusher.Flush()
	}
	return n, err
}

// OpenAIProxy acts as a simple pass-through for OpenAI requests
// since OpenAI endpoints are already OpenAI-compatible.
type OpenAIProxy struct {
	OpenAIEngine *openai.OpenAIEngine
}

// TransformChatCompletionRequest passes through OpenAI requests with minimal transformation.
func (p *OpenAIProxy) TransformChatCompletionRequest(reqBody openai_schema.IncomingChatCompletionRequest) ([]byte, error) {
	logrus.Infof("Processing OpenAI request (Model: %s)", reqBody.Model)

	// Clean up model name by removing openai/ prefix if present
	modelName := strings.TrimPrefix(reqBody.Model, "openai/")
	logrus.Debugf("Model name after prefix removal: %s", modelName)
	reqBody.Model = modelName

	// Since it's OpenAI, we just pass through the request
	transformedBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling OpenAI request: %w", err)
	}

	logrus.Debugf("OpenAI request body: %s", string(transformedBody))
	return transformedBody, nil
}

// HandleChatCompletionRequest sends the request to the OpenAI endpoint.
func (p *OpenAIProxy) HandleChatCompletionRequest(ctx context.Context, model string, stream bool, transformedBody []byte) (*http.Response, error) {
	// Use OpenAI endpoint
	endpoint := "/v1/chat/completions"

	// Create the HTTP request using the engine's backend URL
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(transformedBody))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	// Set headers and apply authentication/modification logic from the engine
	req.Header.Set("Content-Type", "application/json")
	p.OpenAIEngine.ModifyRequest(req) // This adds Bearer token authentication and sets the correct URL

	// Build the full URL
	url := fmt.Sprintf("%s://%s%s", req.URL.Scheme, req.URL.Host, req.URL.Path)

	// Create a new request with the full URL
	finalReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(transformedBody))
	if err != nil {
		return nil, fmt.Errorf("error creating final request: %w", err)
	}

	// Copy headers from the modified request
	for key, values := range req.Header {
		for _, value := range values {
			finalReq.Header.Add(key, value)
		}
	}

	logrus.Debugf("OpenAI endpoint URL: %s", url)

	// Execute the request
	client := &http.Client{
		Timeout: 120 * time.Second,
	}
	logrus.Infof("Sending request to OpenAI endpoint...")
	return client.Do(finalReq)
}

// SendChatCompletionResponse forwards the OpenAI response to the client.
func (p *OpenAIProxy) SendChatCompletionResponse(openaiResp *http.Response, w http.ResponseWriter, stream bool) error {
	defer openaiResp.Body.Close()

	logrus.Infof("Received response from OpenAI (Status: %d, Stream: %t)", openaiResp.StatusCode, stream)

	for key, values := range openaiResp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Set status code
	w.WriteHeader(openaiResp.StatusCode)

	if stream {
		logrus.Info("Forwarding streaming response to client")
		// For streaming, we need to forward chunks as they arrive immediately
		if flusher, ok := w.(http.Flusher); ok {
			// Use io.Copy with a flushWriter that flushes after every write
			flushWriter := &flushWriter{writer: w, flusher: flusher}
			_, err := io.Copy(flushWriter, openaiResp.Body)
			if err != nil {
				logrus.Errorf("Error copying streaming response: %v", err)
				return fmt.Errorf("error copying streaming response: %w", err)
			}
		} else {
			logrus.Warn("ResponseWriter does not support flushing for streaming")
			// Fallback to copying the entire response
			_, err := io.Copy(w, openaiResp.Body)
			return err
		}
	} else {
		logrus.Info("Forwarding non-streaming response to client")
		// For non-streaming, copy the response body directly
		_, err := io.Copy(w, openaiResp.Body)
		if err != nil {
			logrus.Errorf("Error copying response body: %v", err)
			return fmt.Errorf("error copying response body: %w", err)
		}
	}

	logrus.Info("Successfully forwarded OpenAI response to client")
	return nil
}
