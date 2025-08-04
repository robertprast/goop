package bedrock

import (
	"testing"

	"github.com/robertprast/goop/pkg/openai_schema"
)

func TestBuildThinkingConfig(t *testing.T) {
	tests := []struct {
		name           string
		reasoningEffort *string
		expectedTokens int
		expectNil      bool
	}{
		{
			name:           "nil reasoning_effort",
			reasoningEffort: nil,
			expectNil:      true,
		},
		{
			name:           "low reasoning_effort",
			reasoningEffort: stringPtr("low"),
			expectedTokens: 2048,
			expectNil:      false,
		},
		{
			name:           "medium reasoning_effort",
			reasoningEffort: stringPtr("medium"),
			expectedTokens: 8192,
			expectNil:      false,
		},
		{
			name:           "high reasoning_effort",
			reasoningEffort: stringPtr("high"),
			expectedTokens: 32768,
			expectNil:      false,
		},
		{
			name:           "unknown reasoning_effort defaults to medium",
			reasoningEffort: stringPtr("unknown"),
			expectedTokens: 8192,
			expectNil:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := openai_schema.IncomingChatCompletionRequest{
				ReasoningEffort: tt.reasoningEffort,
			}

			result := buildThinkingConfig(reqBody)

			if tt.expectNil {
				if result != nil {
					t.Errorf("Expected nil result, got %+v", result)
				}
				return
			}

			if result == nil {
				t.Errorf("Expected non-nil result, got nil")
				return
			}

			if result.Type != "enabled" {
				t.Errorf("Expected Type to be 'enabled', got %s", result.Type)
			}

			if result.BudgetTokens != tt.expectedTokens {
				t.Errorf("Expected BudgetTokens to be %d, got %d", tt.expectedTokens, result.BudgetTokens)
			}
		})
	}
}

func stringPtr(s string) *string {
	return &s
}