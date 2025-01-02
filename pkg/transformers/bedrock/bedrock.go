package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/robertprast/goop/pkg/openai_schema"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/sirupsen/logrus"
)

type BedrockProxy struct {
	*bedrock.BedrockEngine
}

func (e *BedrockProxy) SendChatCompletionResponse(bedrockResp *http.Response, w http.ResponseWriter, stream bool) error {
	if bedrockResp.Header.Get("Content-Type") == "application/vnd.amazon.eventstream" {
		return e.handleStreamingResponse(bedrockResp, w)
	}
	return e.handleResponse(bedrockResp, w)
}

func (e *BedrockProxy) TransformChatCompletionRequest(reqBody openai_schema.IncomingChatCompletionRequest) ([]byte, error) {
	var systemMessage []bedrock.SystemMessage
	messages := transformMessages(reqBody.Messages)
	if messages == nil {
		return nil, nil
	}
	if messages[0].Role == "system" {
		systemMessage = []bedrock.SystemMessage{
			{Text: messages[0].Content[0].Text},
		}
		messages = messages[1:]
	} else {
		systemMessage = []bedrock.SystemMessage{
			{Text: "You are an assistant."},
		}
	}
	bedrockRequest := bedrock.Request{
		Messages:        messages,
		InferenceConfig: buildInferenceConfig(reqBody),
		System:          systemMessage,
	}

	toolConfig := buildToolConfig(reqBody)
	if toolConfig != nil && len(toolConfig.Tools) > 0 {
		bedrockRequest.ToolConfig = toolConfig
	}

	return json.Marshal(bedrockRequest)
}

func (e *BedrockProxy) handleResponse(bedrockResp *http.Response, w http.ResponseWriter) error {
	logrus.Infof("Sending non-streaming response back")
	logrus.Infof("Bedrock response status: %s", bedrockResp.Status)

	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(bedrockResp.Body)

	var bedrockBody bedrock.Response
	if err := json.NewDecoder(bedrockResp.Body).Decode(&bedrockBody); err != nil {
		logrus.Infof("Error decoding Bedrock response: %v", err)
		return err
	}
	openAIResp := createOpenAIResponse(bedrockBody)
	return sendOpenAIResponse(openAIResp, w)
}

func (e *BedrockProxy) handleStreamingResponse(bedrockResp *http.Response, w http.ResponseWriter) error {
	logrus.Info("Sending streaming response back")
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(bedrockResp.Body)

	decoder := eventstream.NewDecoder()
	var payloadBuf []byte

	for {
		event, err := decoder.Decode(bedrockResp.Body, payloadBuf)
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		logrus.Infof("Received streaming event event: %v", event)
		logrus.Debugf("Event payload: %s", string(event.Payload))

		if err := processStreamingEvent(event, w); err != nil {
			return err
		}
	}

	return nil
}

func (e *BedrockProxy) HandleChatCompletionRequest(ctx context.Context, model string, stream bool, transformedBody []byte) (*http.Response, error) {
	model, found := strings.CutPrefix(model, "bedrock/")
	if !found {
		return nil, fmt.Errorf("error parsing model: %s", model)
	}

	endpoint := fmt.Sprintf("%s/model/%s/%s", e.Backend.String(), model, getEndpointSuffix(stream))
	logrus.Infof("Bedrock endpoint: %s", endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(transformedBody))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	e.SignRequest(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making HTTP request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logrus.Errorf("Bedrock API error: Status %d, Body: %s", resp.StatusCode, string(body))
		resp.Body = io.NopCloser(bytes.NewBuffer(body))
	}

	return resp, nil
}

func getEndpointSuffix(stream bool) string {
	if stream {
		return "converse-stream"
	}
	return "converse"
}
