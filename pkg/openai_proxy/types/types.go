package openai_types

import (
	"encoding/json"
	"errors"
)

type InconcomingChatCompletionRequest struct {
	Model            string         `json:"model"`                       // The model to use (e.g., "gpt-4").
	Messages         []ChatMessage  `json:"messages"`                    // An array of messages in the conversation.
	Temperature      *float64       `json:"temperature,omitempty"`       // Sampling temperature (0-2).
	TopP             *float64       `json:"top_p,omitempty"`             // Top-p sampling (0-1).
	N                *int           `json:"n,omitempty"`                 // Number of completions to generate.
	Stream           bool           `json:"stream"`                      // Whether to stream results.
	Stop             *string        `json:"stop,omitempty"`              // Stop sequence for response generation.
	MaxTokens        *int           `json:"max_tokens,omitempty"`        // Maximum number of tokens to generate.
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`  // Penalty for new topics.
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"` // Penalty for repeated phrases.
	User             *string        `json:"user,omitempty"`              // User identifier for personalization.
	Tools            []FunctionTool `json:"tools,omitempty"`
	ToolChoice       interface{}    `json:"tool_choice,omitempty"` // Controls which (if any) tool is called by the model.
}

type ChatMessage struct {
	Role    string     `json:"role"`              // The role of the message sender ("system", "user", "assistant").
	Content *string    `json:"content,omitempty"` // The text content of the message (optional if image is present).
	Image   *ChatImage `json:"image,omitempty"`   // An image associated with the message (optional if content is present).
	Name    *string    `json:"name,omitempty"`    // Optional name of the user.
}

type ChatImage struct {
	URL     string  `json:"url"`                // URL pointing to the image location.
	Caption *string `json:"caption,omitempty"`  // Optional caption for the image.
	AltText *string `json:"alt_text,omitempty"` // Optional alt text describing the image.
}

type FunctionTool struct {
	Type     string          `json:"type"`     // Type of the tool, e.g., "function".
	Function FunctionDetails `json:"function"` // Details of the function tool.
}

type FunctionDetails struct {
	Name        string                 `json:"name"`        // Name of the function.
	Description string                 `json:"description"` // Description of the function.
	Parameters  map[string]interface{} `json:"parameters"`  // Parameters schema for the function.
}

type ToolConfig struct {
	Tools      []Tool     `json:"tools"`
	ToolChoice ToolChoice `json:"toolChoice,omitempty"`
}

type Tool struct {
	ToolSpec ToolSpec `json:"tool_spec"`
}

type ToolSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"input_schema"`
}

type InputSchema struct {
	JSON interface{} `json:"json"`
}

type ToolChoice struct {
	Auto *struct{} `json:"auto,omitempty"`
	Any  *struct{} `json:"any,omitempty"`
	Tool *ToolName `json:"tool,omitempty"`
}

type ToolName struct {
	Name string `json:"name"`
}

// Custom UnmarshalJSON for InconcomingChatCompletionRequest
func (r *InconcomingChatCompletionRequest) UnmarshalJSON(data []byte) error {
	type Alias InconcomingChatCompletionRequest
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(r),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	// Validate that Messages is not nil
	if r.Messages == nil {
		return errors.New("'messages' field must not be null")
	}

	return nil
}
