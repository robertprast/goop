package openai_schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

type Model struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type IncomingChatCompletionRequest struct {
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
	Tools            []FunctionTool `json:"tools,omitempty"`             // Tools available for the model.
	ToolChoice       interface{}    `json:"tool_choice,omitempty"`       // Controls which (if any) tool is called by the model.
}

type ChatMessage struct {
	Role     string        `json:"role"`                // The role of the message sender ("system", "user", "assistant").
	Type     *string       `json:"type,omitempty"`      // Type of the message (e.g., "image_url").
	Content  *string       `json:"content,omitempty"`   // The text content of the message (optional if image is present).
	ImageURL *ChatImageURL `json:"image_url,omitempty"` // An image associated with the message (optional if content is present).
	Name     *string       `json:"name,omitempty"`      // Optional name of the user.
}

type ChatImageURL struct {
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

// UnmarshalJSON Custom UnmarshalJSON for IncomingChatCompletionRequest
// to validate that the Messages field is not nil and perform additional validations.
func (r *IncomingChatCompletionRequest) UnmarshalJSON(data []byte) error {
	type Alias IncomingChatCompletionRequest
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(r),
	}

	// Unmarshal into the auxiliary struct
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	// Validate that Messages is not nil
	if r.Messages == nil {
		return errors.New("'messages' field must not be null")
	}

	// Validate that Messages is not empty
	if len(r.Messages) == 0 {
		return errors.New("'messages' field must contain at least one message")
	}

	// Validate each ChatMessage
	for i, msg := range r.Messages {
		// Validate Role
		if msg.Role == "" {
			return fmt.Errorf("message at index %d is missing the 'role' field", i)
		}
		validRoles := map[string]bool{
			"system":    true,
			"user":      true,
			"assistant": true,
			// Add other valid roles if any
		}
		if !validRoles[msg.Role] {
			return fmt.Errorf("message at index %d has an invalid 'role': %s", i, msg.Role)
		}

		// Validate based on Type
		if msg.Type != nil && *msg.Type == "image_url" {
			// For image messages, ImageURL must not be nil
			if msg.ImageURL == nil {
				return fmt.Errorf("message at index %d of type 'image_url' must have 'image_url' field", i)
			}
			// Validate URL is not empty
			if msg.ImageURL.URL == "" {
				return fmt.Errorf("message at index %d has an empty 'url' in 'image_url'", i)
			}
			// Validate URL format
			if _, err := url.ParseRequestURI(msg.ImageURL.URL); err != nil {
				return fmt.Errorf("message at index %d has an invalid URL in 'image_url': %v", i, err)
			}
		} else {
			// For non-image messages, Content must not be nil or empty
			if msg.Content == nil || *msg.Content == "" {
				return fmt.Errorf("message at index %d must have 'content' field when 'type' is not 'image_url'", i)
			}
		}
	}

	return nil
}
