package bedrock

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/openai_schema"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/sirupsen/logrus"
)

func buildToolConfig(reqBody openai_schema.IncomingChatCompletionRequest) *bedrock.ToolConfig {
	if len(reqBody.Tools) == 0 {
		return nil
	}

	toolConfig := &bedrock.ToolConfig{
		Tools: make([]bedrock.Tool, len(reqBody.Tools)),
	}

	for i, tool := range reqBody.Tools {
		// Ensure tool name and description are provided to prevent Bedrock API validation errors.
		if tool.Function.Name == "" {
			tool.Function.Name = "default_function_name"
		}
		if tool.Function.Description == "" {
			tool.Function.Description = "Default description for the function"
		}

		toolConfig.Tools[i] = bedrock.Tool{
			ToolSpec: bedrock.ToolSpec{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				InputSchema: bedrock.InputSchema{
					JSON: tool.Function.Parameters,
				},
			},
		}
	}

	switch choice := reqBody.ToolChoice.(type) {
	case string:
		switch choice {
		case "auto":
			toolConfig.ToolChoice = bedrock.ToolChoice{Auto: &struct{}{}}
		case "required":
			toolConfig.ToolChoice = bedrock.ToolChoice{Any: &struct{}{}}
		}
	case map[string]interface{}:
		if tool, ok := choice["function"].(map[string]interface{}); ok {
			if name, ok := tool["name"].(string); ok {
				toolConfig.ToolChoice = bedrock.ToolChoice{Tool: &bedrock.ToolName{Name: name}}
			}
		}
	}
	return toolConfig
}

// transformMessages converts the OpenAI-style messages into Bedrock-compatible messages.
func transformMessages(messages []openai_schema.ChatMessage) []bedrock.Message {
	bedrockMessages := make([]bedrock.Message, len(messages))
	for i, message := range messages {
		var contentBlocks []bedrock.ContentBlock

		if message.Content != nil {
			contentBlocks = append(contentBlocks, bedrock.ContentBlock{
				Text: *message.Content,
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

	content, toolCall, err := extractContentOrToolCall(payload.Delta)
	if err != nil {
		return err
	}

	openAIChunk := createOpenAIChunk(content, toolCall)
	return sendOpenAIChunk(openAIChunk, w)
}

func extractContentOrToolCall(delta json.RawMessage) (string, *bedrock.ToolCall, error) {
	var textDelta bedrock.TextDelta
	if err := json.Unmarshal(delta, &textDelta); err == nil {
		return textDelta.Value, nil, nil
	}

	var toolCall bedrock.ToolCall
	if err := json.Unmarshal(delta, &toolCall); err == nil {
		return "", &toolCall, nil
	}

	return "", nil, fmt.Errorf("failed to unmarshal delta")
}

func getEventType(headers []eventstream.Header) string {
	for _, header := range headers {
		if header.Name == ":event-type" {
			return header.Value.String()
		}
	}
	return ""
}
