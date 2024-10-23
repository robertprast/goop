package bedrock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	openai_types "github.com/robertprast/goop/pkg/openai_proxy/types"
	"github.com/sirupsen/logrus"
)

func buildToolConfig(reqBody openai_types.InconcomingChatCompletionRequest) *ToolConfig {
	if len(reqBody.Tools) == 0 {
		return nil
	}

	toolConfig := &ToolConfig{
		Tools: make([]Tool, len(reqBody.Tools)),
	}

	for i, tool := range reqBody.Tools {
		// Ensure tool name and description are provided to prevent Bedrock API validation errors.
		if tool.Function.Name == "" {
			tool.Function.Name = "default_function_name"
		}
		if tool.Function.Description == "" {
			tool.Function.Description = "Default description for the function"
		}

		toolConfig.Tools[i] = Tool{
			ToolSpec: ToolSpec{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				InputSchema: InputSchema{
					JSON: tool.Function.Parameters,
				},
			},
		}
	}

	switch choice := reqBody.ToolChoice.(type) {
	case string:
		switch choice {
		case "auto":
			toolConfig.ToolChoice = ToolChoice{Auto: &struct{}{}}
		case "required":
			toolConfig.ToolChoice = ToolChoice{Any: &struct{}{}}
		}
	case map[string]interface{}:
		if tool, ok := choice["function"].(map[string]interface{}); ok {
			if name, ok := tool["name"].(string); ok {
				toolConfig.ToolChoice = ToolChoice{Tool: &ToolName{Name: name}}
			}
		}
	}
	return toolConfig
}

// transformMessages converts the OpenAI-style messages into Bedrock-compatible messages.
func transformMessages(messages []openai_types.ChatMessage) []Message {
	bedrockMessages := make([]Message, len(messages))
	for i, message := range messages {
		contentBlocks := []ContentBlock{}

		if message.Content != nil {
			contentBlocks = append(contentBlocks, ContentBlock{
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

		bedrockMessages[i] = Message{
			Role:    message.Role,
			Content: contentBlocks,
		}
	}
	return bedrockMessages
}

// buildInferenceConfig generates a Bedrock-compatible inference configuration from the OpenAI proxy request.
func buildInferenceConfig(reqBody openai_types.InconcomingChatCompletionRequest) InferenceConfig {
	config := InferenceConfig{}
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

func (e *BedrockEngine) handleStreamingResponse(bedrockResp *http.Response, w http.ResponseWriter) error {
	logrus.Info("Sending streaming response back")
	defer bedrockResp.Body.Close()

	decoder := eventstream.NewDecoder()
	var payloadBuf []byte

	for {
		event, err := decoder.Decode(bedrockResp.Body, payloadBuf)
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		logrus.Infof("Received event: %v", event)
		logrus.Infof("Event payload: %s", string(event.Payload))

		if err := processStreamingEvent(event, w); err != nil {
			return err
		}
	}

	return nil
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
	var payload CustomContentBlockDeltaEvent
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

func extractContentOrToolCall(delta json.RawMessage) (string, *ToolCall, error) {
	var textDelta TextDelta
	if err := json.Unmarshal(delta, &textDelta); err == nil {
		return textDelta.Value, nil, nil
	}

	var toolCall ToolCall
	if err := json.Unmarshal(delta, &toolCall); err == nil {
		return "", &toolCall, nil
	}

	return "", nil, fmt.Errorf("failed to unmarshal delta")
}

func createOpenAIChunk(content string, toolCall *ToolCall) map[string]interface{} {

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

func (e *BedrockEngine) handleNonStreamingResponse(bedrockResp *http.Response, w http.ResponseWriter) error {
	logrus.Infof("Sending non-streaming response back")
	defer bedrockResp.Body.Close()
	logrus.Infof("Bedrock response status: %s", bedrockResp.Status)

	var bedrockBody BedrockResponse
	if err := json.NewDecoder(bedrockResp.Body).Decode(&bedrockBody); err != nil {
		logrus.Infof("Error decoding Bedrock response: %v", err)
		return err
	}

	logrus.Infof("Bedrock resp %v", bedrockBody)
	// logrus.Infof("Raw response from bedrock: %v", bedrockResp.Body)
	// print raw bedrcokResp body

	openAIResp := createOpenAIResponse(bedrockBody)
	return sendOpenAIResponse(openAIResp, w)
}

func createOpenAIResponse(bedrockBody BedrockResponse) map[string]interface{} {
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
