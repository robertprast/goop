package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robertprast/goop/pkg/engine/gemini"
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

// GeminiProxy acts as a simple pass-through for Gemini AI requests
// since Google now provides OpenAI-compatible endpoints.
type GeminiProxy struct {
	GeminiEngine *gemini.GeminiEngine
}

// TransformChatCompletionRequest passes through OpenAI requests with minimal transformation
// since Google's endpoint is now OpenAI-compatible.
func (p *GeminiProxy) TransformChatCompletionRequest(reqBody openai_schema.IncomingChatCompletionRequest) ([]byte, error) {
	logrus.Infof("Processing OpenAI request for Gemini AI (Model: %s)", reqBody.Model)

	// Clean up model name by removing gemini/ prefix if present
	modelName := strings.TrimPrefix(reqBody.Model, "gemini/")
	logrus.Debugf("Model name after prefix removal: %s", modelName)
	reqBody.Model = modelName

	transformedBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling Gemini request: %w", err)
	}

	logrus.Debugf("Gemini AI request body: %s", string(transformedBody))
	return transformedBody, nil
}

// HandleChatCompletionRequest sends the request to Google's OpenAI-compatible endpoint.
func (p *GeminiProxy) HandleChatCompletionRequest(ctx context.Context, model string, stream bool, transformedBody []byte) (*http.Response, error) {
	// Create the HTTP request with the path that the engine expects
	// The engine will modify the URL to point to the correct Google endpoint
	endpoint := "/gemini/v1/chat/completions"
	
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(transformedBody))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	// Set headers and let the engine handle URL modification and authentication
	req.Header.Set("Content-Type", "application/json")
	p.GeminiEngine.ModifyRequest(req) // This sets the correct URL and adds Bearer token

	// Build the final URL from the modified request
	finalURL := req.URL.String()
	if req.URL.Scheme == "" {
		finalURL = fmt.Sprintf("%s://%s%s", req.URL.Scheme, req.URL.Host, req.URL.Path)
		if req.URL.Host == "" {
			// If scheme and host aren't set, build manually
			finalURL = fmt.Sprintf("https://%s%s", req.Host, req.URL.Path)
		}
	}

	logrus.Debugf("Final Google OpenAI-compatible endpoint URL: %s", finalURL)

	// Create a new request with the final URL and body
	finalReq, err := http.NewRequestWithContext(ctx, http.MethodPost, finalURL, bytes.NewReader(transformedBody))
	if err != nil {
		return nil, fmt.Errorf("error creating final request: %w", err)
	}

	// Copy headers from the modified request
	for key, values := range req.Header {
		for _, value := range values {
			finalReq.Header.Set(key, value)
		}
	}

	// Execute the request
	client := &http.Client{
		Timeout: 120 * time.Second,
	}
	logrus.Infof("Sending request to Google's OpenAI-compatible endpoint...")
	return client.Do(finalReq)
}

// SendChatCompletionResponse forwards the OpenAI-compatible response from Google to the client.
func (p *GeminiProxy) SendChatCompletionResponse(geminiResp *http.Response, w http.ResponseWriter, stream bool) error {
	defer geminiResp.Body.Close()

	logrus.Infof("Received response from Google OpenAI endpoint (Status: %d, Stream: %t)", geminiResp.StatusCode, stream)

	// Since Google's endpoint returns OpenAI-compatible responses, we can forward them directly
	// Copy response headers
	for key, values := range geminiResp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Set status code
	w.WriteHeader(geminiResp.StatusCode)

	// Stream or copy the response body directly
	if stream {
		logrus.Info("Forwarding streaming response to client")
		// For streaming, we need to forward chunks as they arrive immediately
		if flusher, ok := w.(http.Flusher); ok {
			// Use io.Copy with a flushWriter that flushes after every write
			flushWriter := &flushWriter{writer: w, flusher: flusher}
			_, err := io.Copy(flushWriter, geminiResp.Body)
			if err != nil {
				logrus.Errorf("Error copying streaming response: %v", err)
				return fmt.Errorf("error copying streaming response: %w", err)
			}
		} else {
			logrus.Warn("ResponseWriter does not support flushing for streaming")
			// Fallback to copying the entire response
			_, err := io.Copy(w, geminiResp.Body)
			return err
		}
	} else {
		logrus.Info("Forwarding non-streaming response to client")
		// For non-streaming, copy the response body directly
		_, err := io.Copy(w, geminiResp.Body)
		if err != nil {
			logrus.Errorf("Error copying response body: %v", err)
			return fmt.Errorf("error copying response body: %w", err)
		}
	}

	logrus.Info("Successfully forwarded Google OpenAI response to client")
	return nil
}