package bedrock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/robertprast/goop/pkg/engine"
	"github.com/sirupsen/logrus"
)

// Ensure BedrockEngine implements the OpenAIProxyEngine interface
var _ engine.OpenAIProxyEngine = (*BedrockEngine)(nil)

func buildToolConfig(reqBody map[string]interface{}) *ToolConfig {
	tools, hasTools := reqBody["tools"].([]interface{})
	if !hasTools || len(tools) == 0 {
		return nil
	}

	toolConfig := &ToolConfig{}

	if tools, ok := reqBody["tools"].([]interface{}); ok {
		toolConfig.Tools = make([]Tool, len(tools))
		for i, tool := range tools {
			toolMap := tool.(map[string]interface{})
			functionMap := toolMap["function"].(map[string]interface{})
			parametersMap := functionMap["parameters"].(map[string]interface{})

			toolConfig.Tools[i] = Tool{
				ToolSpec: ToolSpec{
					Name:        functionMap["name"].(string),
					Description: functionMap["description"].(string),
					InputSchema: InputSchema{
						JSON: parametersMap,
					},
				},
			}
		}
	}

	if toolChoice, ok := reqBody["tool_choice"]; ok {
		toolConfig.ToolChoice = parseToolChoice(toolChoice)
	} else {
		// Default to "auto" if not specified
		toolConfig.ToolChoice = ToolChoice{Auto: &struct{}{}}
	}

	return toolConfig
}

func parseToolChoice(toolChoice interface{}) ToolChoice {
	tc := ToolChoice{}

	switch v := toolChoice.(type) {
	case string:
		if v == "auto" {
			tc.Auto = &struct{}{}
		} else if v == "any" {
			tc.Any = &struct{}{}
		}
	case map[string]interface{}:
		if _, ok := v["auto"]; ok {
			tc.Auto = &struct{}{}
		} else if _, ok := v["any"]; ok {
			tc.Any = &struct{}{}
		} else if toolName, ok := v["name"].(string); ok {
			tc.Tool = &ToolName{Name: toolName}
		}
	}

	return tc
}

func transformMessages(messages []interface{}) []Message {
	bedrockMessages := make([]Message, len(messages))
	for i, message := range messages {
		msg := message.(map[string]interface{})
		content := []ContentBlock{
			{
				Type: "text",
				Text: msg["content"].(string),
			},
		}
		bedrockMessages[i] = Message{
			Role:    msg["role"].(string),
			Content: content,
		}
	}
	return bedrockMessages
}

func buildInferenceConfig(reqBody map[string]interface{}) InferenceConfig {
	config := InferenceConfig{}
	if maxTokens, ok := reqBody["max_completion_tokens"].(int); ok {
		config.MaxTokens = maxTokens
	}
	if temperature, ok := reqBody["temperature"].(float64); ok {
		config.Temperature = temperature
	} else {
		config.Temperature = 0.7
	}
	if topP, ok := reqBody["top_p"].(float64); ok {
		config.TopP = topP
	} else {
		config.TopP = 1.0
	}
	if stop, ok := reqBody["stop"].([]string); ok {
		config.StopSequences = stop
	}
	return config
}

func getEndpointSuffix(stream bool) string {
	if stream {
		return "converse-stream"
	}
	return "converse"
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
