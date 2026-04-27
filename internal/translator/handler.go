package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/robertprast/goop/internal/provider"
)

// BedrockHandler serves /openai-bedrock/v1/chat/completions: an OpenAI
// chat-completions endpoint that translates to Bedrock Converse. Use this for
// Bedrock models that AWS's OpenAI-compatible (Mantle) endpoint doesn't yet
// cover (Anthropic Claude, Meta Llama, etc.).
//
// We hold the bedrock Provider directly (not a httputil.ReverseProxy) so the
// streaming path can iterate events as they arrive — going through ServeHTTP
// would buffer the whole upstream response into a recorder before flushing
// any byte, defeating SSE.
type BedrockHandler struct {
	provider  provider.Provider
	transport http.RoundTripper
	logger    *slog.Logger
}

// NewBedrockHandler builds the translator handler around the bedrock provider.
// fallbackTransport is used if provider.Transport() is nil (it shouldn't be
// for bedrock, but we're defensive).
func NewBedrockHandler(p provider.Provider, fallbackTransport http.RoundTripper, logger *slog.Logger) *BedrockHandler {
	if logger == nil {
		logger = slog.Default()
	}
	t := p.Transport()
	if t == nil {
		t = fallbackTransport
	}
	if t == nil {
		t = http.DefaultTransport
	}
	return &BedrockHandler{provider: p, transport: t, logger: logger}
}

// ServeHTTP handles POST /openai-bedrock/v1/chat/completions.
func (h *BedrockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":{"message":"method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read_body", err.Error())
		return
	}
	defer r.Body.Close()

	var req OpenAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "parse_body", err.Error())
		return
	}

	modelID := strings.TrimPrefix(req.Model, "bedrock/")
	if modelID == req.Model {
		writeJSONError(w, http.StatusBadRequest, "model_prefix",
			fmt.Sprintf("model %q must start with 'bedrock/' for this endpoint", req.Model))
		return
	}

	bedrockReq, err := TranslateChatToBedrock(&req)
	if err != nil {
		if errors.Is(err, ErrUnsupportedContent) {
			writeJSONError(w, http.StatusBadRequest, "unsupported_content",
				"this endpoint only supports text content; for image/audio/multimodal requests use the Mantle endpoint at /bedrock-openai/v1/chat/completions")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "translate", err.Error())
		return
	}

	bedrockBody, err := json.Marshal(bedrockReq)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "marshal", err.Error())
		return
	}

	// Build the upstream request. We use the same path shape the native
	// bedrock provider serves, then run the provider's Rewrite to pick up
	// host + URL setup; SigV4 signing happens inside the transport.
	suffix := "converse"
	if req.Stream {
		suffix = "converse-stream"
	}
	upstreamPath := fmt.Sprintf("/bedrock/model/%s/%s", modelID, suffix)

	forward, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamPath, bytes.NewReader(bedrockBody))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "build_request", err.Error())
		return
	}
	forward.Header.Set("Content-Type", "application/json")
	forward.Header.Set("Accept", "application/json")
	forward.ContentLength = int64(len(bedrockBody))

	// Synthesize pr.In with the bedrock-prefixed path. The bedrock provider's
	// Rewrite reads pr.In.URL.Path to derive the upstream path; the original
	// inbound URL is /openai-bedrock/... which would route nowhere useful.
	synthIn := r.Clone(r.Context())
	synthIn.URL = &url.URL{Path: upstreamPath}
	pr := &httputil.ProxyRequest{In: synthIn, Out: forward}
	h.provider.Rewrite(pr)

	resp, err := h.transport.RoundTrip(forward)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writeJSONError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// Pass through upstream error verbatim — keeps the AWS error message
		// intact rather than masking it as our own translation error.
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	if req.Stream {
		h.streamResponse(w, resp, modelID)
		return
	}
	h.unaryResponse(w, resp, modelID)
}

func (h *BedrockHandler) unaryResponse(w http.ResponseWriter, resp *http.Response, modelID string) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "read_response", err.Error())
		return
	}
	var br bedrockConverseResponse
	if err := json.Unmarshal(body, &br); err != nil {
		writeJSONError(w, http.StatusBadGateway, "decode_response", err.Error())
		return
	}
	out := bedrockToOpenAIChatCompletion(&br, modelID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *BedrockHandler) streamResponse(w http.ResponseWriter, resp *http.Response, modelID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	flush := func() { _ = rc.Flush() }

	dec := eventstream.NewDecoder()
	var payloadBuf []byte
	now := time.Now().Unix()
	id := fmt.Sprintf("chatcmpl-%d", now)

	for {
		msg, err := dec.Decode(resp.Body, payloadBuf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.logger.Error("eventstream decode", "err", err)
			break
		}
		chunk, done := bedrockEventToOpenAIChunk(msg, id, modelID, now)
		if chunk != nil {
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flush()
		}
		if done {
			fmt.Fprint(w, "data: [DONE]\n\n")
			flush()
			break
		}
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"type": code, "message": msg},
	})
}

// --- Bedrock response shapes ---

type bedrockConverseResponse struct {
	Output struct {
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Text    string `json:"text,omitempty"`
				ToolUse *struct {
					ToolUseID string         `json:"toolUseId"`
					Name      string         `json:"name"`
					Input     map[string]any `json:"input"`
				} `json:"toolUse,omitempty"`
				Thinking *struct {
					Text string `json:"thinking"`
				} `json:"thinking,omitempty"`
			} `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

func bedrockToOpenAIChatCompletion(r *bedrockConverseResponse, modelID string) map[string]any {
	var text strings.Builder
	var toolCalls []map[string]any
	for _, c := range r.Output.Message.Content {
		if c.Text != "" {
			text.WriteString(c.Text)
		}
		if c.ToolUse != nil {
			args, _ := json.Marshal(c.ToolUse.Input)
			toolCalls = append(toolCalls, map[string]any{
				"id":   c.ToolUse.ToolUseID,
				"type": "function",
				"function": map[string]any{
					"name":      c.ToolUse.Name,
					"arguments": string(args),
				},
			})
		}
	}

	message := map[string]any{
		"role":    r.Output.Message.Role,
		"content": text.String(),
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "bedrock/" + modelID,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": r.StopReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     r.Usage.InputTokens,
			"completion_tokens": r.Usage.OutputTokens,
			"total_tokens":      r.Usage.TotalTokens,
		},
	}
}

// bedrockEventToOpenAIChunk maps a single Bedrock streaming event to an
// OpenAI streaming chunk. Returns (nil, true) for end-of-stream.
func bedrockEventToOpenAIChunk(msg eventstream.Message, id, modelID string, created int64) (map[string]any, bool) {
	eventType := ""
	for _, h := range msg.Headers {
		if h.Name == ":event-type" {
			eventType = h.Value.String()
		}
	}

	switch eventType {
	case "messageStop", "metadata":
		return nil, true
	case "contentBlockDelta":
		var payload struct {
			Delta struct {
				Text    string `json:"text,omitempty"`
				ToolUse *struct {
					Input string `json:"input"`
				} `json:"toolUse,omitempty"`
				Thinking *struct {
					Text string `json:"thinking"`
				} `json:"thinking,omitempty"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return nil, false
		}
		delta := map[string]any{}
		if payload.Delta.Text != "" {
			delta["content"] = payload.Delta.Text
		}
		if len(delta) == 0 {
			return nil, false
		}
		return map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   "bedrock/" + modelID,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			}},
		}, false
	}
	return nil, false
}
