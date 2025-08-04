package bedrock

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/robertprast/goop/pkg/openai_schema"
)

func TestBedrockRequestWithThinking(t *testing.T) {
	// Create a sample OpenAI request with reasoning_effort
	openAIRequest := openai_schema.IncomingChatCompletionRequest{
		Model:           "bedrock/anthropic.claude-sonnet-4-20250514-v1:0",
		Messages: []openai_schema.ChatMessage{
			{
				Role:    "user",
				Content: "What is 2+2?",
			},
		},
		MaxTokens:       intPtr(1000),
		Temperature:     floatPtr(0.7),
		ReasoningEffort: stringPtr("high"),
	}

	// Create a proxy instance
	proxy := &BedrockProxy{}

	// Transform the request
	transformedJSON, err := proxy.TransformChatCompletionRequest(openAIRequest)
	if err != nil {
		t.Fatalf("Error transforming request: %v", err)
	}

	// Verify the JSON contains thinking configuration
	var result map[string]interface{}
	if err := json.Unmarshal(transformedJSON, &result); err != nil {
		t.Fatalf("Error unmarshaling transformed JSON: %v", err)
	}

	// Check that thinking configuration is present
	thinking, exists := result["thinking"]
	if !exists {
		t.Errorf("Expected 'thinking' field in transformed request")
		return
	}

	thinkingMap, ok := thinking.(map[string]interface{})
	if !ok {
		t.Errorf("Expected 'thinking' to be a map, got %T", thinking)
		return
	}

	if thinkingMap["type"] != "enabled" {
		t.Errorf("Expected thinking type to be 'enabled', got %v", thinkingMap["type"])
	}

	budgetTokens, ok := thinkingMap["budget_tokens"].(float64) // JSON unmarshals numbers as float64
	if !ok {
		t.Errorf("Expected budget_tokens to be a number, got %T", thinkingMap["budget_tokens"])
		return
	}

	if int(budgetTokens) != 32768 {
		t.Errorf("Expected budget_tokens to be 32768 for 'high' effort, got %v", budgetTokens)
	}

	// Verify the JSON structure
	jsonStr := string(transformedJSON)
	if !strings.Contains(jsonStr, `"thinking"`) {
		t.Errorf("Transformed JSON should contain 'thinking' field")
	}

	if !strings.Contains(jsonStr, `"type":"enabled"`) {
		t.Errorf("Transformed JSON should contain enabled thinking type")
	}

	t.Logf("Transformed JSON: %s", jsonStr)
}

func TestBedrockRequestWithoutThinking(t *testing.T) {
	// Create a sample OpenAI request without reasoning_effort
	openAIRequest := openai_schema.IncomingChatCompletionRequest{
		Model:           "bedrock/anthropic.claude-sonnet-4-20250514-v1:0",
		Messages: []openai_schema.ChatMessage{
			{
				Role:    "user",
				Content: "What is 2+2?",
			},
		},
		MaxTokens:   intPtr(1000),
		Temperature: floatPtr(0.7),
		// No ReasoningEffort specified
	}

	// Create a proxy instance
	proxy := &BedrockProxy{}

	// Transform the request
	transformedJSON, err := proxy.TransformChatCompletionRequest(openAIRequest)
	if err != nil {
		t.Fatalf("Error transforming request: %v", err)
	}

	// Verify the JSON does not contain thinking configuration
	jsonStr := string(transformedJSON)
	if strings.Contains(jsonStr, `"thinking"`) {
		t.Errorf("Transformed JSON should not contain 'thinking' field when reasoning_effort is not specified: %s", jsonStr)
	}

	t.Logf("Transformed JSON without thinking: %s", jsonStr)
}

func intPtr(i int) *int {
	return &i
}

func floatPtr(f float64) *float64 {
	return &f
}