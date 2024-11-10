package bedrock

import (
	"encoding/json"
	"fmt"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/sirupsen/logrus"
	"net/http"
	"time"
)

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
