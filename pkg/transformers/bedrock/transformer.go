package bedrock

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/openai_schema"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/sirupsen/logrus"
)

func buildToolConfig(reqBody openai_schema.IncomingChatCompletionRequest) *bedrock.ToolConfig {
	// --- Keep your existing buildToolConfig implementation ---
	// (Ensure it correctly handles tool.Function.Name checks as per previous fixes if needed)
	if len(reqBody.Tools) == 0 {
		return nil
	}
	logrus.Infof("Building tool config from request body: %v", reqBody)
	toolConfig := &bedrock.ToolConfig{
		Tools: make([]bedrock.Tool, 0, len(reqBody.Tools)), // Initialize with 0 length, capacity len
	}
	for i, tool := range reqBody.Tools {
		if tool.Type != "function" {
			logrus.Warnf("Skipping non-function tool type: %s", tool.Type)
			continue
		}
		// Validate function details
		funcName := tool.Function.Name
		funcDesc := tool.Function.Description
		if funcName == "" {
			logrus.Warnf("Tool index %d has type 'function' but empty Name. Skipping.", i)
			// Or assign a default if Bedrock *requires* it, but skipping is safer.
			// funcName = fmt.Sprintf("unnamed_function_%d", i)
			continue // Skip tool with no name
		}
		// Bedrock might require description, add default if needed and allowed
		if funcDesc == "" {
			logrus.Warnf("Tool '%s' has empty description, using default.", funcName)
			funcDesc = "No description provided." // Or handle as error if required
		}

		// Ensure parameters are valid JSON Schema (map[string]interface{})
		// We assume reqBody.Tools[i].Function.Parameters is already this type
		parametersSchema, ok := tool.Function.Parameters.(map[string]interface{})
		if !ok && tool.Function.Parameters != nil {
			logrus.Errorf("Tool '%s' parameters are not a valid JSON Schema object (map[string]interface{}). Type: %T. Skipping.", funcName, tool.Function.Parameters)
			continue // Skip if parameters are malformed
		}
		if ok && len(parametersSchema) == 0 {
			// Handle empty parameters map if necessary, Bedrock might require at least {}
			logrus.Debugf("Tool '%s' has empty parameters map.", funcName)
			// parametersSchema = map[string]interface{}{} // Ensure it's an empty object, not nil
		}

		toolConfig.Tools = append(toolConfig.Tools, bedrock.Tool{
			ToolSpec: bedrock.ToolSpec{
				Name:        funcName,
				Description: funcDesc,
				InputSchema: bedrock.InputSchema{
					// Ensure parametersSchema is correctly passed (it should be map[string]interface{} or nil)
					JSON: parametersSchema,
				},
			},
		})
	}

	// If no valid tools were added, return nil
	if len(toolConfig.Tools) == 0 {
		logrus.Warnf("No valid function tools found to build Bedrock tool config.")
		return nil
	}

	// Handle ToolChoice
	var toolChoice bedrock.ToolChoice = bedrock.ToolChoice{Auto: &struct{}{}} // Default to auto
	switch choice := reqBody.ToolChoice.(type) {
	case string:
		switch choice {
		case "auto":
			toolChoice.Auto = &struct{}{}
		case "any": // OpenAI "required" often maps to Bedrock "any"
			logrus.Infof("Mapping OpenAI tool_choice 'required' to Bedrock 'any'.")
			toolChoice.Any = &struct{}{}
			toolChoice.Auto = nil // Explicitly nil Auto if Any is set
		case "none":
			// Bedrock doesn't have a direct "none". Auto is the closest default.
			logrus.Warnf("OpenAI tool_choice 'none' not directly supported by Bedrock, using 'auto'.")
			toolChoice.Auto = &struct{}{}
		default:
			logrus.Warnf("Unknown string tool_choice '%s', defaulting to 'auto'.", choice)
			toolChoice.Auto = &struct{}{}
		}
	case map[string]interface{}:
		// Handle specific tool choice: { "type": "function", "function": { "name": "my_func" } }
		if choiceType, ok := choice["type"].(string); ok && choiceType == "function" {
			if tool, ok := choice["function"].(map[string]interface{}); ok {
				if name, ok := tool["name"].(string); ok && name != "" {
					logrus.Infof("Setting Bedrock tool_choice to specific tool: %s", name)
					toolChoice.Tool = &bedrock.ToolName{Name: name}
					toolChoice.Auto = nil // Explicitly nil Auto if Tool is set
				} else {
					logrus.Warnf("Tool choice specified function but name was missing or empty, defaulting to 'auto'.")
				}
			} else {
				logrus.Warnf("Tool choice 'function' format invalid, defaulting to 'auto'.")
			}
		} else {
			logrus.Warnf("Unsupported tool_choice object format, defaulting to 'auto'.")
		}
	default:
		if reqBody.ToolChoice != nil { // Log only if it was provided but unhandled type
			logrus.Warnf("Unsupported type for tool_choice: %T, defaulting to 'auto'.", reqBody.ToolChoice)
		}
		// Default is already auto
	}
	toolConfig.ToolChoice = toolChoice

	logrus.Infof("Built tool config: %+v", toolConfig)
	return toolConfig
}

// transformMessages converts the OpenAI-style messages into Bedrock-compatible messages.
func transformMessages(messages []openai_schema.ChatMessage) []bedrock.Message {
	bedrockMessages := make([]bedrock.Message, len(messages))
	for i, message := range messages {
		if message.Content == nil {
			logrus.Warnf("Message %d has nil content, skipping.", i)
			continue
		}

		var contentBlocks []bedrock.ContentBlock
		switch content := message.Content.(type) {
		case string:
			contentBlocks = append(contentBlocks, bedrock.ContentBlock{
				Text: content,
			})
		}

		if message.Type != nil && *message.Type == "image_url" {
			resp, err := http.Get(message.ImageURL.URL)
			if err != nil {
				panic(err)
			}
			imageBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				panic(err)
			}
			var textContent string
			if imageBytes != nil {
				textContent = base64.StdEncoding.EncodeToString(imageBytes)
			}
			contentBlocks = append(contentBlocks, bedrock.ContentBlock{
				Image: &bedrock.Image{
					Format: "jpeg",
					Source: bedrock.ImageSource{
						Bytes: textContent,
					},
				},
			})
		}

		bedrockMessages[i] = bedrock.Message{
			Role:    message.Role,
			Content: contentBlocks,
		}
	}
	return bedrockMessages
}

// buildInferenceConfig generates a Bedrock-compatible inference configuration from the OpenAI engine_proxy request.
func buildInferenceConfig(reqBody openai_schema.IncomingChatCompletionRequest) bedrock.InferenceConfig {
	config := bedrock.InferenceConfig{}
	if reqBody.MaxTokens != nil {
		config.MaxTokens = *reqBody.MaxTokens
	}
	if reqBody.Temperature != nil {
		config.Temperature = *reqBody.Temperature
	} else {
		config.Temperature = 0.7
	}
	if reqBody.TopP != nil {
		config.TopP = *reqBody.TopP
	} else {
		config.TopP = 1.0
	}
	if reqBody.Stop != nil {
		config.StopSequences = []string{*reqBody.Stop}
	}
	return config
}

// buildThinkingConfig generates a Bedrock-compatible thinking configuration from the OpenAI reasoning_effort parameter.
func buildThinkingConfig(reqBody openai_schema.IncomingChatCompletionRequest) *bedrock.ThinkingConfig {
	if reqBody.ReasoningEffort == nil {
		return nil
	}

	// Map reasoning_effort levels to appropriate token budgets
	var budgetTokens int
	switch *reqBody.ReasoningEffort {
	case "low":
		budgetTokens = 2048
	case "medium":
		budgetTokens = 8192
	case "high":
		budgetTokens = 32768
	default:
		logrus.Warnf("Unknown reasoning_effort level: %s, defaulting to medium", *reqBody.ReasoningEffort)
		budgetTokens = 8192
	}

	return &bedrock.ThinkingConfig{
		Type:         "enabled",
		BudgetTokens: budgetTokens,
	}
}

func processStreamingEvent(event eventstream.Message, w http.ResponseWriter) error {
	eventType := getEventType(event.Headers)
	switch eventType {
	case "messageStart":
		// No action needed
	case "messageEnd":
		_, err := w.Write([]byte("[DONE]\n\n"))
		if err != nil {
			return err
		}
	case "contentBlockDelta":
		return handleContentBlockDelta(event, w)
	default:
		logrus.Warnf("Unknown event type: %s", eventType)
	}
	return nil
}

func handleContentBlockDelta(event eventstream.Message, w http.ResponseWriter) error {
	var payload bedrock.CustomContentBlockDeltaEvent
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		logrus.Warnf("Error unmarshaling payload: %v", err)
		return nil
	}
	logrus.Infof("Raw response from bedrock: %v", string(payload.Delta))

	content, toolCall, thinking, err := extractContentOrToolCall(payload.Delta)
	if err != nil {
		return err
	}

	openAIChunk := createOpenAIChunk(content, toolCall, thinking)
	return sendOpenAIChunk(openAIChunk, w)
}

func extractContentOrToolCall(delta json.RawMessage) (string, *bedrock.ToolCall, string, error) {
	var textDelta bedrock.TextDelta
	if err := json.Unmarshal(delta, &textDelta); err == nil {
		return textDelta.Value, nil, "", nil
	}

	var toolCall bedrock.ToolCall
	if err := json.Unmarshal(delta, &toolCall); err == nil {
		return "", &toolCall, "", nil
	}

	// Try to unmarshal as thinking delta
	var thinkingDelta bedrock.Thinking
	if err := json.Unmarshal(delta, &thinkingDelta); err == nil {
		return "", nil, thinkingDelta.Text, nil
	}

	return "", nil, "", fmt.Errorf("failed to unmarshal delta")
}

func getEventType(headers []eventstream.Header) string {
	for _, header := range headers {
		if header.Name == ":event-type" {
			return header.Value.String()
		}
	}
	return ""
}
