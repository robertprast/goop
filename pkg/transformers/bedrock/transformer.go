package bedrock

import (
	"encoding/json"
	"fmt"
	"github.com/robertprast/goop/pkg/proxy/openai_schema/types"
	"net/http"
	"time"

	"github.com/robertprast/goop/pkg/engine/bedrock"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/sirupsen/logrus"
)

func buildToolConfig(reqBody openai_types.IncomingChatCompletionRequest) *bedrock.ToolConfig {
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
func transformMessages(messages []openai_types.ChatMessage) []bedrock.Message {
	bedrockMessages := make([]bedrock.Message, len(messages))
	for i, message := range messages {
		contentBlocks := []bedrock.ContentBlock{}

		if message.Content != nil {
			contentBlocks = append(contentBlocks, bedrock.ContentBlock{
				Type: "text",
				Text: *message.Content,
			})
		}

		// <TODO> Fix ContentBlock types
		// if message.Image != nil {
		// 	contentBlocks = append(contentBlocks, ContentBlock{
		// 		Type: "image",
		// 		URL:  message.Image.URL,
		// 		Caption: func() string {
		// 			if message.Image.Caption != nil {
		// 				return *message.Image.Caption
		// 			}
		// 			return ""
		// 		}(),
		// 	})
		// }

		bedrockMessages[i] = bedrock.Message{
			Role:    message.Role,
			Content: contentBlocks,
		}
	}
	return bedrockMessages
}

// buildInferenceConfig generates a Bedrock-compatible inference configuration from the OpenAI engine_proxy request.
func buildInferenceConfig(reqBody openai_types.IncomingChatCompletionRequest) bedrock.InferenceConfig {
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
		w.Write([]byte("[DONE]\n\n"))
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

func createOpenAIChunk(content string, toolCall *bedrock.ToolCall) map[string]interface{} {

	delta := map[string]interface{}{}
	if content != "" {
		delta["content"] = content
	}
	if toolCall != nil {
		delta["tool_calls"] = []map[string]interface{}{
			{
				"index": 0,
				"id":    toolCall.ID,
				"type":  toolCall.Type,
				"function": map[string]interface{}{
					"name":      toolCall.Function.Name,
					"arguments": toolCall.Function.Arguments,
				},
			},
		}
	}

	return map[string]interface{}{
		"id":      "chatcmpl-" + time.Now().Format("20060102150405"),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   "bedrock-claude",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			},
		},
	}
}

func sendOpenAIChunk(openAIChunk map[string]interface{}, w http.ResponseWriter) error {
	chunkJSON, err := json.Marshal(openAIChunk)
	if err != nil {
		logrus.Infof("Error marshaling OpenAI chunk: %v", err)
		return err
	}

	dataStr := fmt.Sprintf("data: %s\n\n", string(chunkJSON))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("x-request-id", openAIChunk["id"].(string))

	logrus.Infof("OpenAI chunk: %s", string(dataStr))

	if _, err := w.Write([]byte(dataStr)); err != nil {
		return err
	}
	w.(http.Flusher).Flush()
	return nil
}

func createOpenAIResponse(bedrockBody bedrock.Response) map[string]interface{} {
	messageContent := ""
	var toolCalls []map[string]interface{}

	for _, item := range bedrockBody.Output.Message.Content {
		if item.Text != "" {
			messageContent += item.Text
		}
		if item.ToolUse != nil {
			toolCall := map[string]interface{}{
				"id":   item.ToolUse.ToolUseId,
				"type": "function",
				"function": map[string]interface{}{
					"name":      item.ToolUse.Name,
					"arguments": item.ToolUse.Input,
				},
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	message := map[string]interface{}{
		"role":    bedrockBody.Output.Message.Role,
		"content": messageContent,
	}

	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	return map[string]interface{}{
		"id":      "chatcmpl-" + time.Now().Format("20060102150405"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "bedrock-claude",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": bedrockBody.StopReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     bedrockBody.Usage.InputTokens,
			"completion_tokens": bedrockBody.Usage.OutputTokens,
			"total_tokens":      bedrockBody.Usage.TotalTokens,
		},
	}
}

func sendOpenAIResponse(openAIResp map[string]interface{}, w http.ResponseWriter) error {
	responseBody, err := json.Marshal(openAIResp)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(responseBody)
	return err
}

func getEventType(headers []eventstream.Header) string {
	for _, header := range headers {
		if header.Name == ":event-type" {
			return header.Value.String()
		}
	}
	return ""
}
