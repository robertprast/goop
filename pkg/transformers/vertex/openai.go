package vertex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	// Removed: "github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream" // No longer needed for this format
	"github.com/sirupsen/logrus"
	// Added: "bufio" was removed, ensure other necessary imports like "encoding/json" are present.
)

// Helper to convert an io.Reader (like decoder.Buffered()) to bytes
func streamToBytes(stream io.Reader) []byte {
	buf := new(bytes.Buffer)
	// It's safe to ignore errors here in this specific context,
	// as ReadFrom typically only returns errors from the underlying reader,
	// and bytes.Buffer itself doesn't error on Write.
	_, _ = buf.ReadFrom(stream)
	return buf.Bytes()
}

// handleStreamingResponse processes streaming responses from Vertex AI (JSON array stream format)
// and converts them to OpenAI SSE format.
func handleStreamingResponse(vertexResp *http.Response, w http.ResponseWriter) error {
	if vertexResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(vertexResp.Body)
		defer vertexResp.Body.Close()
		return fmt.Errorf("vertex API returned error status: %d, body: %s", vertexResp.StatusCode, string(body))
	}
	logrus.Infof("Handling Vertex AI streaming response (JSON array format)")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	logrus.Info("Sending streaming response back using SSE format")

	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logrus.Errorf("Error closing Vertex response body: %v", err)
		}
	}(vertexResp.Body)

	// Use json.Decoder to parse the streamed JSON array
	decoder := json.NewDecoder(vertexResp.Body)

	// 1. Consume the opening bracket '[' of the JSON array
	t, err := decoder.Token()
	if err != nil {
		if err == io.EOF {
			logrus.Warn("Vertex stream was empty")
			return sendFinalChunk(w)
		}
		// If we get an error reading the *first* token, the body might contain an error message.
		// Try reading the rest for logging.
		bodyBytes, _ := io.ReadAll(io.MultiReader(decoder.Buffered(), vertexResp.Body))
		logrus.Errorf("Error reading start of Vertex stream: %v. Body content: %s", err, string(bodyBytes))
		return fmt.Errorf("error reading start of Vertex stream: %w", err)
	}

	// --- Start of Corrected Block ---
	if delim, ok := t.(json.Delim); !ok || delim != '[' {
		// If the stream doesn't start with '[', it's an unexpected format.
		// Read the rest of the body (buffered data + remaining stream) to log the unexpected content.
		// The unexpected token 't' itself might not be easily converted back to raw bytes,
		// but we can log its value and type.
		bufferedData := decoder.Buffered()              // Get data already read by the decoder
		remainingData, _ := io.ReadAll(vertexResp.Body) // Read the rest from the original source
		// Combine what we can for logging
		fullUnexpectedBody := append(streamToBytes(bufferedData), remainingData...) // Combine buffered + remaining

		// Log token info + body start (limit length if body is huge)
		logBody := string(fullUnexpectedBody)
		if len(logBody) > 500 { // Limit log length
			logBody = logBody[:500] + "..."
		}
		logrus.Errorf("Expected '[' at start of Vertex stream, but got token type %T with value '%v'. Full unexpected body start: %s", t, t, logBody)
		// Return a more user-friendly error if possible, or the technical one
		return fmt.Errorf("unexpected start of Vertex stream: expected '[', got %T value '%v'", t, t)
	}
	// --- End of Corrected Block ---

	logrus.Debug("Consumed opening '[' from Vertex stream")

	// 2. Loop through the JSON objects within the array
	chunkIndex := 0 // Add index for better logging
	for decoder.More() {
		var vertexChunk map[string]interface{}
		if err := decoder.Decode(&vertexChunk); err != nil {
			if err == io.EOF { // EOF *during* decode usually means incomplete JSON object
				logrus.Error("EOF reached unexpectedly while decoding an object within the Vertex stream array")
				return fmt.Errorf("unexpected EOF decoding vertex chunk in array: %w", err)
			}
			// Log context around the error
			buffered := decoder.Buffered()
			remainingBytes, _ := io.ReadAll(buffered) // Read what's left in the buffer
			logrus.Errorf("Error decoding Vertex JSON object at index %d from stream: %v. Content near error: %s", chunkIndex, err, string(remainingBytes))
			return fmt.Errorf("error decoding vertex chunk at index %d: %w", chunkIndex, err)
		}

		logrus.Debugf("Successfully decoded Vertex chunk index %d: %+v", chunkIndex, vertexChunk)

		openAIChunk := transformVertexChunkToOpenAI(vertexChunk)
		if openAIChunk == nil {
			logrus.Debugf("Transformed chunk index %d is nil, skipping sending.", chunkIndex)
			chunkIndex++
			continue
		}

		logrus.Debugf("Transformed OpenAI chunk index %d: %+v", chunkIndex, openAIChunk)

		if err := sendChunk(openAIChunk, w); err != nil {
			logrus.Errorf("Error sending chunk index %d to client: %v", chunkIndex, err)
			return fmt.Errorf("error sending chunk index %d to client: %w", chunkIndex, err)
		}
		logrus.Debugf("Sent chunk index %d to client", chunkIndex)
		chunkIndex++ // Increment index after successful send
	} // End of loop for decoder.More()

	// 3. Consume the closing bracket ']' (optional verification step)
	t, err = decoder.Token()
	// We expect EOF or the closing delimiter ']' here.
	if err != nil && err != io.EOF {
		// Error occurred *after* the loop, maybe trailing invalid data?
		logrus.Warnf("Error consuming token after loop (expected ']' or EOF): %v", err)
	} else if delim, ok := t.(json.Delim); ok && delim == ']' {
		logrus.Debug("Consumed closing ']' from Vertex stream")
	} else if err != io.EOF { // Log if it wasn't EOF and wasn't ']'
		// This case might happen if there's extra data after the array, like whitespace or garbage.
		logrus.Warnf("Expected ']' or EOF at end of Vertex stream array, but got token %T %v", t, t)
	} else {
		// If err == io.EOF here, it means the stream ended right after the last element, which is valid JSON.
		logrus.Debug("Stream ended (EOF) after processing array elements.")
	}

	logrus.Info("Vertex stream finished, sending [DONE] message to client")
	// 4. Send the final [DONE] message for SSE protocol
	if err := sendFinalChunk(w); err != nil {
		logrus.Errorf("Error sending final [DONE] chunk: %v", err)
		return fmt.Errorf("error sending final chunk: %w", err)
	}

	logrus.Info("Finished handling Vertex AI streaming response")
	return nil
}

// --- Rest of the functions remain the same ---

// transformVertexChunkToOpenAI processes an individual Vertex AI response chunk
// and converts it to OpenAI streaming format
func transformVertexChunkToOpenAI(vertexChunk map[string]interface{}) map[string]interface{} {
	candidates, ok := vertexChunk["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		if _, usageOk := vertexChunk["usageMetadata"]; usageOk {
			logrus.Debug("Received Vertex chunk with usageMetadata but no candidates, skipping transformation for content.")
			return nil
		}
		logrus.Warnf("Vertex chunk missing candidates or candidates array is empty: %v", vertexChunk)
		return nil
	}

	candidateMap, ok := candidates[0].(map[string]interface{})
	if !ok {
		logrus.Warnf("First candidate is not a valid map: %v", candidates[0])
		return nil
	}

	content, contentOk := extractContentText(candidateMap)

	var toolCalls []map[string]interface{}
	functionCall, hasFunctionCall := extractFunctionCall(candidateMap)
	if hasFunctionCall {
		toolCalls = []map[string]interface{}{
			{
				"index":    0,
				"id":       fmt.Sprintf("call_%d", time.Now().UnixNano()),
				"type":     "function",
				"function": functionCall,
			},
		}
		logrus.Debugf("Extracted function call: %v", functionCall)
	}

	delta := map[string]interface{}{}
	hasContentOrToolCall := false
	if contentOk && content != "" {
		delta["content"] = content
		hasContentOrToolCall = true
		logrus.Debugf("Extracted content delta: %s", content)
	}
	if toolCalls != nil {
		delta["tool_calls"] = toolCalls
		hasContentOrToolCall = true
		logrus.Debugf("Extracted tool_calls delta: %v", toolCalls)
	}

	var finishReason interface{} = nil
	reasonStr, hasFinishReason := candidateMap["finishReason"].(string)
	if hasFinishReason && reasonStr != "" {
		finishReason = mapFinishReason(reasonStr)
		logrus.Debugf("Extracted finish reason: %s -> %v", reasonStr, finishReason)
	}

	// Only create a chunk if there's content, a tool call, or a finish reason.
	// Avoid sending empty chunks unless it's the very last one signifying the end.
	if !hasContentOrToolCall && finishReason == nil {
		logrus.Debug("Chunk has no content, tool calls, or finish reason. Skipping.")
		return nil
	}

	return map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   "vertex-gemini", // Or derive from request/config
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
}

// extractContentText extracts text content from a Vertex AI response candidate
func extractContentText(candidate map[string]interface{}) (string, bool) {
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return "", false
	}
	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return "", false
	}
	var textContent strings.Builder
	foundText := false
	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		if text, ok := partMap["text"].(string); ok {
			textContent.WriteString(text)
			foundText = true
		}
	}
	return textContent.String(), foundText
}

// extractFunctionCall extracts function call information from a Vertex AI response candidate
func extractFunctionCall(candidate map[string]interface{}) (map[string]interface{}, bool) {
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return nil, false
	}

	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		functionCallData, ok := partMap["functionCall"].(map[string]interface{})
		if !ok {
			continue
		}

		name, nameOk := functionCallData["name"].(string)
		args, argsOk := functionCallData["args"].(map[string]interface{})

		if nameOk {
			var argsJSON string = "{}"
			var marshalErr error
			if argsOk {
				argsBytes, err := json.Marshal(args)
				if err != nil {
					logrus.Warnf("Failed to marshal function args: %v. Args data: %v", err, args)
					marshalErr = err
				} else {
					argsJSON = string(argsBytes)
				}
			}
			if marshalErr != nil {
				logrus.Errorf("Using default empty args due to marshalling error for function call '%s'", name)
			}

			return map[string]interface{}{
				"name":      name,
				"arguments": argsJSON,
			}, true
		}
	}

	return nil, false
}

// sendChunk sends a chunk in OpenAI SSE format
func sendChunk(chunk map[string]interface{}, w http.ResponseWriter) error {
	if chunk == nil {
		logrus.Warn("Attempted to send a nil chunk")
		return nil
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		logrus.Errorf("Error marshalling OpenAI chunk: %v", err)
		return fmt.Errorf("error marshalling chunk: %w", err)
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	if err != nil {
		logrus.Errorf("Error writing chunk to response: %v", err)
		return fmt.Errorf("error writing chunk: %w", err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
		logrus.Debug("Flushed chunk to client")
	} else {
		logrus.Warn("ResponseWriter does not support flushing")
	}
	return nil
}

// sendFinalChunk sends the final chunk in OpenAI SSE format
func sendFinalChunk(w http.ResponseWriter) error {
	_, err := fmt.Fprintf(w, "data: [DONE]\n\n")
	if err != nil {
		logrus.Errorf("Error writing final [DONE] chunk: %v", err)
		return fmt.Errorf("error writing final chunk: %w", err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
		logrus.Debug("Flushed final [DONE] chunk to client")
	} else {
		logrus.Warn("ResponseWriter does not support flushing for final chunk")
	}
	return nil
}

// createOpenAIResponse converts a Vertex AI response to OpenAI format for non-streaming responses
func createOpenAIResponse(vertexResponse map[string]interface{}) map[string]interface{} {
	candidates, ok := vertexResponse["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		logrus.Errorf("Vertex response missing candidates or candidates array is empty: %v", vertexResponse)
		if safetyRatings, ok := vertexResponse["promptFeedback"].(map[string]interface{}); ok {
			if blockReason, ok := safetyRatings["blockReason"].(string); ok {
				return createErrorResponse(fmt.Sprintf("Vertex request blocked due to %s", blockReason))
			}
		}
		return createErrorResponse("Invalid response from Vertex AI: No candidates found")
	}

	candidate := candidates[0].(map[string]interface{})

	message := map[string]interface{}{
		"role": "assistant",
	}

	content, contentOk := extractFullContent(candidate)
	if contentOk {
		message["content"] = content
	} else {
		message["content"] = nil
	}

	functionCall, hasFunctionCall := extractFunctionCall(candidate)
	if hasFunctionCall {
		message["tool_calls"] = []map[string]interface{}{
			{
				"id":       fmt.Sprintf("call_%d", time.Now().UnixNano()),
				"type":     "function",
				"function": functionCall,
			},
		}
		if !contentOk {
			message["content"] = nil
		}
	}

	if !contentOk && !hasFunctionCall {
		if safetyRatings, ok := candidate["safetyRatings"].([]interface{}); ok && len(safetyRatings) > 0 {
			logrus.Warnf("Response candidate has no text content or function call, but has safety ratings: %v", safetyRatings)
		} else if finishReason, ok := candidate["finishReason"].(string); ok && finishReason != "STOP" {
			logrus.Warnf("Response candidate has no text/tool_call, finished due to: %s", finishReason)
		} else {
			logrus.Warnf("Response candidate has no text content or function call, but candidate exists: %v", candidate)
		}
	}

	usage := map[string]interface{}{
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
	if usageMetadata, ok := vertexResponse["usageMetadata"].(map[string]interface{}); ok {
		if promptTokens, ok := usageMetadata["promptTokenCount"].(float64); ok {
			usage["prompt_tokens"] = int(promptTokens)
		}
		if completionTokens, ok := usageMetadata["candidatesTokenCount"].(float64); ok {
			usage["completion_tokens"] = int(completionTokens)
		}
		if totalTokens, ok := usageMetadata["totalTokenCount"].(float64); ok { // Use totalTokenCount if available
			usage["total_tokens"] = int(totalTokens)
			// Infer completion tokens if not directly provided and prompt tokens are known
			if _, ctOk := usageMetadata["candidatesTokenCount"]; !ctOk {
				if pt, ptOk := usage["prompt_tokens"].(int); ptOk && pt > 0 {
					usage["completion_tokens"] = int(totalTokens) - pt
				}
			}
		} else { // Fallback calculation if totalTokenCount is missing
			usage["total_tokens"] = usage["prompt_tokens"].(int) + usage["completion_tokens"].(int)
		}
	}

	finishReason := "stop"
	if reason, ok := candidate["finishReason"].(string); ok && reason != "" {
		finishReason = mapFinishReason(reason)
	} else if !contentOk && !hasFunctionCall {
		if safetyRatings, ok := candidate["safetyRatings"].([]interface{}); ok {
			for _, ratingInterface := range safetyRatings {
				rating, ok := ratingInterface.(map[string]interface{})
				if !ok {
					continue
				}
				if blocked, ok := rating["blocked"].(bool); ok && blocked {
					finishReason = "content_filter"
					logrus.Warnf("Setting finish_reason to content_filter due to blocked safety rating: %v", rating)
					break
				}
			}
		}
	}

	return map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "vertex-gemini",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": usage,
	}
}

// extractFullContent extracts the full content text from a non-streaming candidate
func extractFullContent(candidate map[string]interface{}) (string, bool) {
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		logrus.Debugf("Candidate has no 'content' field or it's not a map: %v", candidate)
		return "", false
	}
	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		logrus.Debugf("Content has no 'parts' field or it's empty: %v", content)
		return "", false
	}

	var fullText strings.Builder
	foundText := false
	for i, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			logrus.Warnf("Part index %d is not a map: %v", i, part)
			continue
		}
		if text, ok := partMap["text"].(string); ok {
			fullText.WriteString(text)
			foundText = true
		} else if _, funcOk := partMap["functionCall"]; funcOk {
			logrus.Debugf("Skipping functionCall part while extracting full text content.")
		} else {
			logrus.Warnf("Part index %d contains neither 'text' nor 'functionCall': %v", i, partMap)
		}
	}

	finalText := fullText.String()
	return finalText, foundText && finalText != ""
}

// mapFinishReason maps Vertex AI finish reasons to OpenAI format
func mapFinishReason(vertexReason string) string {
	switch strings.ToUpper(vertexReason) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	case "RECITATION":
		return "content_filter"
	case "TOOL_CALLS": // Explicitly map TOOL_CALLS if Vertex uses it
		return "tool_calls"
	case "OTHER", "UNKNOWN", "FINISH_REASON_UNSPECIFIED":
		logrus.Warnf("Mapping Vertex finish reason '%s' to 'stop'", vertexReason)
		return "stop"
	default:
		logrus.Warnf("Unknown Vertex finish reason '%s', defaulting to 'stop'", vertexReason)
		return "stop"
	}
}

// createErrorResponse creates an OpenAI-format error response
func createErrorResponse(message string) map[string]interface{} {
	if message == "" {
		message = "An unknown error occurred processing the request with Vertex AI."
	}
	logrus.Errorf("Creating OpenAI error response: %s", message)
	return map[string]interface{}{
		"object": "error",
		"error": map[string]interface{}{
			"message": message,
			"type":    "vertex_ai_error",
			"param":   nil,
			"code":    "vertex_error",
		},
	}
}

// sendOpenAIResponse sends a non-streaming response in OpenAI format
func sendOpenAIResponse(response map[string]interface{}, w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	statusCode := http.StatusOK
	if _, isError := response["error"]; isError {
		statusCode = http.StatusInternalServerError // Default to 500 for proxy errors
		logrus.Errorf("Sending error response with status %d: %v", statusCode, response["error"])
	}

	data, err := json.Marshal(response)
	if err != nil {
		// Log the fatal error before attempting to write a fallback response
		logrus.Errorf("FATAL: Failed to marshal final OpenAI JSON response: %v. Response data: %+v", err, response)
		http.Error(w, `{"error": {"message": "Internal server error: failed to marshal final response", "type": "internal_error", "code": "marshal_error"}}`, http.StatusInternalServerError)
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	w.WriteHeader(statusCode)
	_, err = w.Write(data)
	if err != nil {
		logrus.Errorf("Error writing final OpenAI response to client: %v", err)
		// Don't return the error directly here as the header/status might have already been sent.
		// The calling function might not be able to do anything with it. Just log it.
	} else {
		logrus.Debugf("Successfully sent non-streaming OpenAI response (status %d)", statusCode)
	}
	return err // Return the write error if it occurred
}
