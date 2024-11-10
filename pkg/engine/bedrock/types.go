package bedrock

import (
	"encoding/json"
)

type CustomContentBlockDeltaEvent struct {
	ContentBlockIndex int             `json:"contentBlockIndex"`
	Delta             json.RawMessage `json:"delta"`
}

type TextDelta struct {
	Value string `json:"text"`
}

type ToolUseDelta struct {
	Value string `json:"input"`
}

type Response struct {
	Metrics struct {
		LatencyMs int `json:"latencyMs"`
	} `json:"metrics"`
	Output struct {
		Message struct {
			Content []ContentItem `json:"content"`
			Role    string        `json:"role"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

type ContentItem struct {
	Text    string   `json:"text,omitempty"`
	ToolUse *ToolUse `json:"toolUse,omitempty"`
}

type ToolUse struct {
	Input     map[string]string `json:"input"`
	Name      string            `json:"name"`
	ToolUseId string            `json:"toolUseId"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Request struct {
	Messages        []Message       `json:"messages"`
	InferenceConfig InferenceConfig `json:"inferenceConfig"`
	System          []SystemMessage `json:"system"`
	ToolConfig      *ToolConfig     `json:"toolConfig,omitempty"`
}

type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Text   string `json:"text,omitempty"`
	Format string `json:"format,omitempty"`
	Image  *Image `json:"image,omitempty"`
}

type SystemMessage struct {
	Text string `json:"text"`
}

type Image struct {
	Format string      `json:"format,omitempty"`
	Source ImageSource `json:"source,omitempty"`
}

type ImageSource struct {
	Bytes string `json:"bytes,omitempty"`
}
type InferenceConfig struct {
	Temperature   float64  `json:"temperature,omitempty"`
	TopP          float64  `json:"top_p,omitempty"`
	MaxTokens     int      `json:"max_tokens,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
}

type ToolConfig struct {
	Tools      []Tool     `json:"tools"`
	ToolChoice ToolChoice `json:"toolChoice,omitempty"`
}

type Tool struct {
	ToolSpec ToolSpec `json:"toolSpec"`
}

type ToolSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
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
