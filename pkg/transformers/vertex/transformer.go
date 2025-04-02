package vertex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/robertprast/goop/pkg/engine/vertex" // Assuming this is your internal vertex engine package
	"github.com/robertprast/goop/pkg/openai_schema" // Assuming this has ChatMessage.Content as interface{}
	"github.com/sirupsen/logrus"
)

// VertexProxy acts as the transformer and handler for Vertex AI requests.
type VertexProxy struct {
	VertexEngine *vertex.VertexEngine // Your engine providing base URL and auth modification
}

// --- Main Proxy Methods ---

// TransformChatCompletionRequest converts an OpenAI-style request to the Vertex AI format.
func (p *VertexProxy) TransformChatCompletionRequest(reqBody openai_schema.IncomingChatCompletionRequest) ([]byte, error) {
	logrus.Infof("Transforming OpenAI request for Vertex AI (Model: %s)", reqBody.Model)
	modelName := strings.TrimPrefix(reqBody.Model, "vertex/") // Remove potential prefix
	logrus.Debugf("Model name after prefix removal: %s", modelName)

	// Transform messages using the logic that handles multi-modal content
	vertexMessages, err := transformMessages(reqBody.Messages)
	if err != nil {
		return nil, fmt.Errorf("error transforming messages: %w", err)
	}

	// Check if messages resulted in an empty list, which Vertex might reject
	if len(vertexMessages) == 0 {
		logrus.Warnf("Transformation resulted in an empty message list for Vertex.")
		// Depending on strictness, you might want:
		// return nil, fmt.Errorf("cannot send empty message list to Vertex AI")
	}

	// Build the main request body for Vertex AI
	requestBody := map[string]interface{}{
		"contents":         vertexMessages,
		"generationConfig": buildGenerationConfig(reqBody), // Pass the whole reqBody
		// Note: System instructions are handled differently in Vertex.
	}

	// Add tool configuration if tools are present in the request
	if len(reqBody.Tools) > 0 {
		toolConfig := buildToolConfig(reqBody) // Pass the whole reqBody
		if toolConfig != nil {
			requestBody["tools"] = toolConfig
			logrus.Infof("Added tool configuration to Vertex request.")
		}
	}

	// Marshal the transformed request body to JSON
	transformedBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling transformed Vertex request: %w", err)
	}

	logrus.Debugf("Transformed Vertex AI request body: %s", string(transformedBody))
	return transformedBody, nil
}

// HandleChatCompletionRequest sends the transformed request to the Vertex AI endpoint.
func (p *VertexProxy) HandleChatCompletionRequest(ctx context.Context, model string, stream bool, transformedBody []byte) (*http.Response, error) {
	modelName := strings.TrimPrefix(model, "vertex/")
	projectId := extractProjectID(modelName) // Ensure this retrieves your GCP Project ID
	location := "us-central1"                // Default location - consider making configurable

	// Allow specifying location in model name like "us-west1:gemini-1.5-pro-preview-0409"
	if strings.Contains(modelName, ":") {
		parts := strings.SplitN(modelName, ":", 2)
		if len(parts) == 2 {
			location = parts[0]
			modelName = parts[1] // The actual model ID after the location
		}
	}

	// Validate Project ID before making the call
	if projectId == "" || strings.Contains(projectId, "invalid") || strings.Contains(projectId, "error") || projectId == "your-gcp-project-id" {
		errMsg := fmt.Sprintf("invalid GCP Project ID configured: %s. Set VERTEX_PROJECT_ID env var.", projectId)
		logrus.Error(errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	logrus.Infof("Routing request to Vertex AI - Project: %s, Location: %s, Model: %s, Stream: %t", projectId, location, modelName, stream)

	// Construct the Vertex AI API endpoint URL
	endpointFormat := "/v1beta1/projects/%s/locations/%s/publishers/google/models/%s:%s"
	action := "generateContent"
	if stream {
		action = "streamGenerateContent"
	}
	endpoint := fmt.Sprintf(endpointFormat, projectId, location, modelName, action)

	// Get the base URL (e.g., "https://us-central1-aiplatform.googleapis.com") from the engine
	baseURL := p.VertexEngine.GetBackendURL()
	if baseURL == "" {
		return nil, fmt.Errorf("vertex engine base URL is not configured")
	}
	url := baseURL + endpoint

	logrus.Debugf("Vertex AI request URL: %s", url)

	// Create the HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(transformedBody))
	if err != nil {
		return nil, fmt.Errorf("error creating Vertex AI request: %w", err)
	}

	// Set headers and apply authentication/modification logic from the engine
	req.Header.Set("Content-Type", "application/json")
	p.VertexEngine.ModifyRequest(req) // IMPORTANT: This should add Authorization header (Bearer token)

	// Execute the request using a standard HTTP client
	client := &http.Client{
		Timeout: 120 * time.Second, // Add a reasonable timeout
	}
	logrus.Infof("Sending request to Vertex AI endpoint...")
	return client.Do(req)
}

// SendChatCompletionResponse processes the Vertex AI response and forwards it to the original client.
func (p *VertexProxy) SendChatCompletionResponse(vertexResp *http.Response, w http.ResponseWriter, stream bool) error {
	defer vertexResp.Body.Close() // Ensure body is always closed

	logrus.Infof("Received response from Vertex AI (Status: %d, Stream: %t)", vertexResp.StatusCode, stream)

	// Handle non-OK status codes first
	if vertexResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(vertexResp.Body) // Read body for error details
		logMessage := fmt.Sprintf("Vertex API error. Status: %d, Body: %s", vertexResp.StatusCode, string(bodyBytes))
		logrus.Error(logMessage)

		// Attempt to send an OpenAI-compatible error response back to the client
		w.Header().Set("Content-Type", "application/json")
		statusCode := vertexResp.StatusCode
		// Optional: Map specific upstream errors to standard HTTP errors if desired
		// if statusCode == 401 || statusCode == 403 { ... }
		w.WriteHeader(statusCode) // Mirror upstream status code

		// Use the helper from openai.go (make sure it exists and is imported/accessible)
		errorResp := createErrorResponse(fmt.Sprintf("Upstream Vertex AI Error (Status %d)", vertexResp.StatusCode))
		if err := json.NewEncoder(w).Encode(errorResp); err != nil {
			logrus.Errorf("Failed to encode error response to client: %v", err)
		}
		return fmt.Errorf("vertex API returned error status: %d", vertexResp.StatusCode)
	}

	// Handle successful responses (Status OK)
	if stream {
		logrus.Infof("Streaming response back to client.")
		// Use the dedicated streaming handler (assumed to be in openai.go)
		return handleStreamingResponse(vertexResp, w)
	}

	// Handle non-streaming responses
	logrus.Infof("Processing non-streaming response.")
	body, err := io.ReadAll(vertexResp.Body)
	if err != nil {
		logrus.Errorf("Error reading non-streaming response body: %v", err)
		http.Error(w, "Failed to read upstream response", http.StatusInternalServerError)
		return fmt.Errorf("error reading vertex response body: %w", err)
	}

	logrus.Debugf("Raw non-streaming response from Vertex AI: %s", string(body))

	// Unmarshal the JSON response from Vertex
	var vertexResponse map[string]interface{}
	if err := json.Unmarshal(body, &vertexResponse); err != nil {
		logrus.Errorf("Error unmarshaling non-streaming vertex response: %v. Body: %s", err, string(body))
		// Send an OpenAI-compatible error response
		errorResp := createErrorResponse("Failed to parse upstream Vertex AI response")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(errorResp) // Best effort
		return fmt.Errorf("error unmarshaling vertex response: %w", err)
	}

	// Convert the Vertex response to OpenAI format (use helper from openai.go)
	openAIResp := createOpenAIResponse(vertexResponse)

	// Send the OpenAI-formatted response back to the client (use helper from openai.go)
	logrus.Infof("Sending non-streaming OpenAI-formatted response to client.")
	return sendOpenAIResponse(openAIResp, w)
}

// --- Helper Functions ---

// transformMessages converts OpenAI messages to Vertex AI format, handling multi-modal content.
// Assumes messages []openai_schema.ChatMessage where ChatMessage.Content is interface{}.
func transformMessages(messages []openai_schema.ChatMessage) ([]map[string]interface{}, error) {
	var vertexMessages []map[string]interface{}

	for msgIdx, message := range messages {
		role := mapRole(message.Role)
		if message.Role == "system" {
			logrus.Warnf("System message at index %d mapped to 'user' role for Vertex. Consider using 'systemInstruction'.", msgIdx)
		}

		vertexMessage := map[string]interface{}{
			"role": role,
		}

		var vertexParts []map[string]interface{}

		// Type switch on message.Content (which MUST be interface{} in the struct definition)
		switch content := message.Content.(type) {
		case string:
			if content != "" {
				vertexParts = append(vertexParts, map[string]interface{}{"text": content})
				logrus.Debugf("Msg %d: Processed string content for role '%s'", msgIdx, role)
			} else {
				logrus.Debugf("Msg %d: Empty string content for role '%s'", msgIdx, role)
			}
		case []interface{}:
			logrus.Debugf("Msg %d: Processing multi-part content for role '%s' (%d parts)", msgIdx, role, len(content))
			for partIdx, part := range content {
				partMap, ok := part.(map[string]interface{})
				if !ok {
					logrus.Warnf("Msg %d, Part %d: Skipping invalid part structure in content array (role '%s'): %T", msgIdx, partIdx, role, part)
					continue
				}

				partType, _ := partMap["type"].(string)
				switch partType {
				case "text":
					if text, ok := partMap["text"].(string); ok && text != "" {
						vertexParts = append(vertexParts, map[string]interface{}{"text": text})
					} else if !ok {
						logrus.Warnf("Msg %d, Part %d: Text part content is not a string (role '%s')", msgIdx, partIdx, role)
					}
				case "image_url":
					if imageURLObj, ok := partMap["image_url"].(map[string]interface{}); ok {
						if url, ok := imageURLObj["url"].(string); ok && url != "" {
							mimeType, data, err := processImageURL(url)
							if err != nil {
								logrus.Errorf("Msg %d, Part %d: Failed to process image URL (role '%s') %s: %v", msgIdx, partIdx, role, url, err)
								continue // Skip this image part
							}
							vertexParts = append(vertexParts, map[string]interface{}{
								"inline_data": map[string]interface{}{
									"mime_type": mimeType,
									"data":      data,
								},
							})
							logrus.Debugf("Msg %d, Part %d: Added image part (mime: %s)", msgIdx, partIdx, mimeType)
						} else {
							logrus.Warnf("Msg %d, Part %d: Missing or invalid 'url' in image_url part (role '%s')", msgIdx, partIdx, role)
						}
					} else {
						logrus.Warnf("Msg %d, Part %d: Missing or invalid 'image_url' object in image_url part (role '%s')", msgIdx, partIdx, role)
					}
				default:
					logrus.Warnf("Msg %d, Part %d: Skipping unsupported part type '%s' (role '%s')", msgIdx, partIdx, partType, role)
				}
			}
		case nil:
			logrus.Debugf("Msg %d: Message content is nil (Role: '%s')", msgIdx, role)
		default:
			logrus.Errorf("Msg %d: Unexpected content type received: %T for role '%s'. Value: %v", msgIdx, message.Content, role, message.Content)
		}

		// TODO: Handle Tool Calls / Function Results if your schema supports them

		if len(vertexParts) > 0 {
			vertexMessage["parts"] = vertexParts
			vertexMessages = append(vertexMessages, vertexMessage)
		} else {
			if message.Content != nil {
				logrus.Warnf("Msg %d: Message content was non-nil but resulted in zero parts (Role: '%s', Content: %v)", msgIdx, role, message.Content)
			} else {
				logrus.Debugf("Msg %d: Skipping message with no generated parts (Role: '%s')", msgIdx, role)
			}
		}
	}

	logrus.Infof("Transformed %d OpenAI messages into %d Vertex messages.", len(messages), len(vertexMessages))
	return vertexMessages, nil
}

// processImageURL handles data URIs and remote URLs, returning mime type and base64 data.
func processImageURL(url string) (mimeType string, data string, err error) {
	if strings.HasPrefix(url, "data:") {
		parts := strings.SplitN(url, ",", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid data URI format: missing comma")
		}
		header := parts[0]
		data = parts[1]

		mimeType = "application/octet-stream" // Default
		if strings.HasPrefix(header, "data:") && strings.Contains(header, ";base64") {
			potentialMime := strings.TrimPrefix(header, "data:")
			potentialMime = strings.TrimSuffix(potentialMime, ";base64")
			if potentialMime != "" && strings.Contains(potentialMime, "/") {
				if !strings.HasPrefix(potentialMime, "image/") {
					logrus.Warnf("Processing data URI with non-image mime type: %s", potentialMime)
				}
				mimeType = potentialMime
			} else {
				logrus.Warnf("Could not parse mime type from data URI header '%s', using default.", header)
			}
		} else {
			logrus.Warnf("Data URI header '%s' missing ';base64' or 'data:', using default mime type.", header)
		}
		logrus.Debugf("Processed data URI. Mime: %s", mimeType)
		return mimeType, data, nil // Data is already base64
	}

	// Handle remote URL
	logrus.Debugf("Fetching remote image URL: %s", url)
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", "", fmt.Errorf("http get error for image url %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("failed to fetch image url %s: status %d", url, resp.StatusCode)
	}

	imageBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("error reading image bytes from url %s: %w", url, err)
	}

	// Determine MIME type
	headerContentType := resp.Header.Get("Content-Type")
	cleanedContentType := ""
	if headerContentType != "" {
		if idx := strings.Index(headerContentType, ";"); idx != -1 {
			cleanedContentType = strings.TrimSpace(headerContentType[:idx])
		} else {
			cleanedContentType = strings.TrimSpace(headerContentType)
		}
	}

	if cleanedContentType == "" || cleanedContentType == "application/octet-stream" {
		logrus.Debugf("MIME type from header was '%s', attempting to guess from URL extension.", headerContentType)
		dotIndex := strings.LastIndex(url, ".")
		qIndex := strings.LastIndex(url, "?")
		if qIndex != -1 && (dotIndex == -1 || qIndex < dotIndex) {
			dotIndex = -1
		}

		if dotIndex != -1 && dotIndex < len(url)-1 {
			ext := url[dotIndex:]
			if qIndex := strings.Index(ext, "?"); qIndex != -1 {
				ext = ext[:qIndex]
			}
			if guessedMime := mime.TypeByExtension(ext); guessedMime != "" {
				mimeType = guessedMime
				logrus.Debugf("Guessed MIME type '%s' from extension '%s'", mimeType, ext)
			} else {
				mimeType = "image/jpeg" // Fallback
				logrus.Warnf("Could not guess mime type for extension '%s', defaulting to %s", ext, mimeType)
			}
		} else {
			mimeType = "image/jpeg" // Fallback
			logrus.Warnf("Could not determine mime type for %s (no header/extension), defaulting to %s", url, mimeType)
		}
	} else {
		mimeType = cleanedContentType // Use cleaned header type
	}

	logrus.Infof("Fetched remote image. Mime: %s, Size: %d bytes", mimeType, len(imageBytes))
	data = base64.StdEncoding.EncodeToString(imageBytes)
	return mimeType, data, nil
}

// mapRole converts OpenAI role names to Vertex AI role names.
func mapRole(role string) string {
	switch role {
	case "user":
		return "user"
	case "assistant":
		return "model"
	case "system":
		return "user" // Map system to user as fallback
	case "tool":
		return "function" // Map tool result role to function role
	default:
		logrus.Warnf("Unknown OpenAI role '%s', mapping to 'user' as default.", role)
		return "user"
	}
}

// buildGenerationConfig creates the Vertex AI generationConfig object from OpenAI parameters.
// *** CORRECTED Stop handling ***
func buildGenerationConfig(reqBody openai_schema.IncomingChatCompletionRequest) map[string]interface{} {
	config := map[string]interface{}{}

	if reqBody.MaxTokens != nil && *reqBody.MaxTokens > 0 {
		config["maxOutputTokens"] = *reqBody.MaxTokens
	}
	if reqBody.Temperature != nil {
		config["temperature"] = *reqBody.Temperature
	}
	if reqBody.TopP != nil {
		config["topP"] = *reqBody.TopP
	}
	// if reqBody.TopK != nil { config["topK"] = *reqBody.TopK } // Uncomment if needed

	// Handle Stop Sequence (reqBody.Stop is *string)
	if reqBody.Stop != nil && *reqBody.Stop != "" {
		// Vertex expects an array of strings
		stopSequence := *reqBody.Stop
		config["stopSequences"] = []string{stopSequence}
		logrus.Debugf("Added stopSequence: [%s]", stopSequence)
	} else {
		// Log if stop was present but empty
		if reqBody.Stop != nil {
			logrus.Debugf("Ignoring empty stop sequence string.")
		}
	}

	if len(config) > 0 {
		logrus.Debugf("Built generationConfig: %v", config)
	}
	return config
}

// buildToolConfig creates the Vertex AI 'tools' configuration from OpenAI tools.
// *** CORRECTED Function check ***
func buildToolConfig(reqBody openai_schema.IncomingChatCompletionRequest) []map[string]interface{} {
	if len(reqBody.Tools) == 0 {
		return nil
	}

	var functionDeclarations []map[string]interface{}
	for i, tool := range reqBody.Tools {
		// Check type first
		if tool.Type == "function" {
			// Since Function is a struct, not a pointer, we access its fields directly.
			// The primary validation is that the Name exists.
			if tool.Function.Name == "" {
				logrus.Warnf("Skipping tool index %d: Type is 'function' but Function.Name is empty.", i)
				continue
			}

			// Assume tool.Function.Parameters is already the correct map[string]interface{} structure for JSON Schema
			declaration := map[string]interface{}{
				"name":        tool.Function.Name,
				"description": tool.Function.Description, // Optional but recommended
				"parameters":  tool.Function.Parameters,  // MUST be JSON Schema Object
			}
			functionDeclarations = append(functionDeclarations, declaration)
			logrus.Debugf("Added function declaration for tool: %s", tool.Function.Name)

		} else {
			logrus.Warnf("Skipping tool index %d: Unsupported tool type '%s'.", i, tool.Type)
		}
	}

	// Vertex wraps function declarations in a specific structure within the 'tools' array
	if len(functionDeclarations) > 0 {
		vertexTools := []map[string]interface{}{
			{
				"functionDeclarations": functionDeclarations,
			},
		}
		logrus.Infof("Built tool configuration with %d function declarations.", len(functionDeclarations))
		return vertexTools
	}

	logrus.Warnf("OpenAI tools were provided, but none resulted in valid Vertex function declarations.")
	return nil
}

// extractProjectID retrieves the GCP Project ID.
func extractProjectID(modelName string) string {
	envProjectID := os.Getenv("VERTEX_PROJECT_ID")
	if envProjectID != "" {
		logrus.Debugf("Using Project ID from environment variable VERTEX_PROJECT_ID.")
		return envProjectID
	}

	// Fallback - ** REPLACE OR REMOVE FOR PRODUCTION **
	defaultProjectID := "encrypted-llm"
	logrus.Warnf("VERTEX_PROJECT_ID environment variable not set. Using hardcoded fallback: '%s'. Configure this for production.", defaultProjectID)

	// Final check on the fallback value itself
	if defaultProjectID == "" || defaultProjectID == "your-gcp-project-id" {
		logrus.Errorf("CRITICAL: GCP Project ID is not configured correctly (fallback is invalid). Set VERTEX_PROJECT_ID environment variable.")
		return "invalid-project-id-not-configured"
	}
	return defaultProjectID
}
