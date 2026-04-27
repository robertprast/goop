// Package translator translates OpenAI-format chat-completion requests to
// provider-specific formats. The Bedrock Converse translator exists as a
// fallback for models that AWS's OpenAI-compatible (Mantle) endpoint does
// not yet cover (Anthropic Claude, Meta Llama, etc.). For models on Mantle,
// route through internal/provider's bedrock-openai provider instead — it
// avoids translation entirely.
package translator

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupportedContent is returned when a chat message contains content
// parts (e.g. images) that the Bedrock Converse translator can't represent.
// Callers should respond 400 and direct users to the Mantle endpoint
// (/bedrock-openai/) which handles multimodal natively.
var ErrUnsupportedContent = errors.New("unsupported content type for the translator (use /bedrock-openai/ for multimodal)")

// OpenAIChatRequest is the strict subset of the OpenAI Chat Completions
// schema we look at. We deliberately keep this small — the goal is to pluck
// fields out of an inbound request, not to maintain a full schema mirror.
type OpenAIChatRequest struct {
	Model           string          `json:"model"`
	Messages        []OpenAIMessage `json:"messages"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	MaxTokens       *int            `json:"max_tokens,omitempty"`
	Stop            json.RawMessage `json:"stop,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	Tools           []OpenAITool    `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	ReasoningEffort *string         `json:"reasoning_effort,omitempty"`
}

// OpenAIMessage models a chat message. Content can be a string or an array
// of content parts; we keep it as RawMessage and resolve at translate time.
type OpenAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Name    *string         `json:"name,omitempty"`
}

// OpenAITool models the function-tool wrapper.
type OpenAITool struct {
	Type     string            `json:"type"`
	Function OpenAIFunctionDef `json:"function"`
}

// OpenAIFunctionDef is the function schema half of a tool.
type OpenAIFunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// BedrockRequest is the Converse-API request body.
type BedrockRequest struct {
	Messages        []BedrockMessage   `json:"messages"`
	System          []BedrockSystem    `json:"system,omitempty"`
	InferenceConfig BedrockInferConfig `json:"inferenceConfig"`
	ToolConfig      *BedrockToolConfig `json:"toolConfig,omitempty"`
	Thinking        *BedrockThinking   `json:"thinking,omitempty"`
}

// BedrockMessage matches the Converse schema.
type BedrockMessage struct {
	Role    string         `json:"role"`
	Content []BedrockBlock `json:"content"`
}

// BedrockBlock is a single message content block. Only Text is populated by
// the translator; image handling is intentionally not implemented here
// (use Mantle for multimodal — it accepts OpenAI-format image URLs natively).
type BedrockBlock struct {
	Text string `json:"text,omitempty"`
}

// BedrockSystem is a system-prompt block.
type BedrockSystem struct {
	Text string `json:"text"`
}

// BedrockInferConfig holds inference parameters.
type BedrockInferConfig struct {
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	MaxTokens     *int     `json:"maxTokens,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

// BedrockToolConfig wraps the tool list and choice.
type BedrockToolConfig struct {
	Tools      []BedrockTool      `json:"tools"`
	ToolChoice *BedrockToolChoice `json:"toolChoice,omitempty"`
}

// BedrockTool is a single tool spec.
type BedrockTool struct {
	ToolSpec BedrockToolSpec `json:"toolSpec"`
}

// BedrockToolSpec is the tool definition body.
type BedrockToolSpec struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	InputSchema BedrockInputSchema `json:"inputSchema"`
}

// BedrockInputSchema wraps the JSON Schema.
type BedrockInputSchema struct {
	JSON any `json:"json"`
}

// BedrockToolChoice mirrors Converse's exclusive choice union.
type BedrockToolChoice struct {
	Auto *struct{}        `json:"auto,omitempty"`
	Any  *struct{}        `json:"any,omitempty"`
	Tool *BedrockToolName `json:"tool,omitempty"`
}

// BedrockToolName names a specific tool.
type BedrockToolName struct {
	Name string `json:"name"`
}

// BedrockThinking is the extended-thinking config.
type BedrockThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// TranslateChatToBedrock converts an OpenAI chat-completion request to a
// Bedrock Converse-API request. Errors only on truly malformed inputs;
// ignored or partially-mapped fields are warnings the caller can log.
func TranslateChatToBedrock(req *OpenAIChatRequest) (*BedrockRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages: empty")
	}

	out := &BedrockRequest{
		InferenceConfig: buildInferenceConfig(req),
	}

	// Hoist leading system messages.
	idx := 0
	for idx < len(req.Messages) && req.Messages[idx].Role == "system" {
		text, err := plainText(req.Messages[idx].Content)
		if err != nil {
			return nil, fmt.Errorf("message %q: %w", req.Messages[idx].Role, err)
		}
		if text != "" {
			out.System = append(out.System, BedrockSystem{Text: text})
		}
		idx++
	}

	// Convert remaining messages.
	for _, m := range req.Messages[idx:] {
		text, err := plainText(m.Content)
		if err != nil {
			return nil, fmt.Errorf("message %q: %w", m.Role, err)
		}
		if text == "" {
			continue
		}
		out.Messages = append(out.Messages, BedrockMessage{
			Role:    m.Role,
			Content: []BedrockBlock{{Text: text}},
		})
	}

	if tc := buildToolConfig(req); tc != nil {
		out.ToolConfig = tc
	}
	if t := buildThinking(req); t != nil {
		out.Thinking = t
	}
	return out, nil
}

// plainText returns the textual content of a message. It accepts:
//   - a JSON string ("hello")
//   - an array of content parts; all parts must be type=="text" and are
//     concatenated. Any non-text part returns ErrUnsupportedContent so the
//     caller can fail loudly rather than silently dropping an image.
//   - JSON null / missing → ""
func plainText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content: unsupported shape: %s", raw)
	}
	var out strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", fmt.Errorf("content part type %q: %w", p.Type, ErrUnsupportedContent)
		}
		out.WriteString(p.Text)
	}
	return out.String(), nil
}

func buildInferenceConfig(req *OpenAIChatRequest) BedrockInferConfig {
	cfg := BedrockInferConfig{
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
	}
	if len(req.Stop) > 0 {
		var single string
		if err := json.Unmarshal(req.Stop, &single); err == nil && single != "" {
			cfg.StopSequences = []string{single}
		} else {
			var multi []string
			if err := json.Unmarshal(req.Stop, &multi); err == nil {
				cfg.StopSequences = multi
			}
		}
	}
	return cfg
}

func buildToolConfig(req *OpenAIChatRequest) *BedrockToolConfig {
	if len(req.Tools) == 0 {
		return nil
	}
	tools := make([]BedrockTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.Type != "function" || t.Function.Name == "" {
			continue
		}
		tools = append(tools, BedrockTool{
			ToolSpec: BedrockToolSpec{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: BedrockInputSchema{JSON: t.Function.Parameters},
			},
		})
	}
	if len(tools) == 0 {
		return nil
	}
	return &BedrockToolConfig{
		Tools:      tools,
		ToolChoice: translateToolChoice(req.ToolChoice),
	}
}

func translateToolChoice(raw json.RawMessage) *BedrockToolChoice {
	if len(raw) == 0 {
		return &BedrockToolChoice{Auto: &struct{}{}}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto", "":
			return &BedrockToolChoice{Auto: &struct{}{}}
		case "required":
			return &BedrockToolChoice{Any: &struct{}{}}
		case "none":
			return nil // omit toolConfig.toolChoice; closest semantic
		}
	}
	var named struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &named); err == nil && named.Type == "function" && named.Function.Name != "" {
		return &BedrockToolChoice{Tool: &BedrockToolName{Name: named.Function.Name}}
	}
	return &BedrockToolChoice{Auto: &struct{}{}}
}

func buildThinking(req *OpenAIChatRequest) *BedrockThinking {
	if req.ReasoningEffort == nil {
		return nil
	}
	budget := 8192
	switch *req.ReasoningEffort {
	case "low":
		budget = 2048
	case "medium":
		budget = 8192
	case "high":
		budget = 32768
	}
	return &BedrockThinking{Type: "enabled", BudgetTokens: budget}
}
