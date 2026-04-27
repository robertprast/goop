package translator

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBuildThinking(t *testing.T) {
	tests := []struct {
		name           string
		effort         *string
		expectedTokens int
		expectNil      bool
	}{
		{"nil reasoning_effort", nil, 0, true},
		{"low", strPtr("low"), 2048, false},
		{"medium", strPtr("medium"), 8192, false},
		{"high", strPtr("high"), 32768, false},
		{"unknown defaults to medium", strPtr("xxx"), 8192, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildThinking(&OpenAIChatRequest{ReasoningEffort: tt.effort})
			if tt.expectNil {
				if got != nil {
					t.Fatalf("want nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want non-nil, got nil")
			}
			if got.Type != "enabled" {
				t.Errorf("Type: want enabled, got %q", got.Type)
			}
			if got.BudgetTokens != tt.expectedTokens {
				t.Errorf("BudgetTokens: want %d, got %d", tt.expectedTokens, got.BudgetTokens)
			}
		})
	}
}

func TestTranslateChatToBedrock_RequiresMessages(t *testing.T) {
	if _, err := TranslateChatToBedrock(&OpenAIChatRequest{}); err == nil {
		t.Fatal("want error for empty messages, got nil")
	}
}

func TestTranslateChatToBedrock_HoistsSystem(t *testing.T) {
	req := &OpenAIChatRequest{
		Model: "anthropic.claude",
		Messages: []OpenAIMessage{
			{Role: "system", Content: json.RawMessage(`"be helpful"`)},
			{Role: "user", Content: json.RawMessage(`"hello"`)},
		},
	}
	out, err := TranslateChatToBedrock(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.System) != 1 || out.System[0].Text != "be helpful" {
		t.Errorf("system: %+v", out.System)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Errorf("messages: %+v", out.Messages)
	}
}

func TestTranslateChatToBedrock_ContentPartsImageRejected(t *testing.T) {
	req := &OpenAIChatRequest{
		Messages: []OpenAIMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"b"}]`)},
		},
	}
	_, err := TranslateChatToBedrock(req)
	if err == nil {
		t.Fatal("want error for image content part, got nil")
	}
	if !errors.Is(err, ErrUnsupportedContent) {
		t.Fatalf("err: want ErrUnsupportedContent, got %v", err)
	}
}

func TestTranslateChatToBedrock_ContentPartsTextOnly(t *testing.T) {
	req := &OpenAIChatRequest{
		Messages: []OpenAIMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)},
		},
	}
	out, err := TranslateChatToBedrock(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("messages: %+v", out.Messages)
	}
	if got := out.Messages[0].Content[0].Text; got != "ab" {
		t.Errorf("text concat: want %q, got %q", "ab", got)
	}
}

func TestTranslateToolChoice(t *testing.T) {
	cases := []struct {
		input string
		want  string // "auto", "any", "tool:foo", "nil"
	}{
		{`""`, "auto"},
		{`"auto"`, "auto"},
		{`"required"`, "any"},
		{`"none"`, "nil"},
		{`{"type":"function","function":{"name":"foo"}}`, "tool:foo"},
	}
	for _, c := range cases {
		got := translateToolChoice(json.RawMessage(c.input))
		var label string
		switch {
		case got == nil:
			label = "nil"
		case got.Auto != nil:
			label = "auto"
		case got.Any != nil:
			label = "any"
		case got.Tool != nil:
			label = "tool:" + got.Tool.Name
		}
		if label != c.want {
			t.Errorf("%s: want %s, got %s", c.input, c.want, label)
		}
	}
}

func TestTranslateChatToBedrock_JSONShape(t *testing.T) {
	req := &OpenAIChatRequest{
		Messages:        []OpenAIMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		ReasoningEffort: strPtr("high"),
	}
	out, err := TranslateChatToBedrock(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"thinking"`) {
		t.Errorf("missing thinking in %s", b)
	}
	if !strings.Contains(string(b), `"type":"enabled"`) {
		t.Errorf("missing enabled in %s", b)
	}
}

func strPtr(s string) *string { return &s }
