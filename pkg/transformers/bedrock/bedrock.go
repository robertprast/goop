package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/robertprast/goop/pkg/proxy/openai_schema/types"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/sirupsen/logrus"
)

type BedrockProxy struct {
	*bedrock.BedrockEngine
}

func (e *BedrockProxy) SendChatCompletionResponse(bedrockResp *http.Response, w http.ResponseWriter, stream bool) error {
	logrus.Infof("Sending request to bedrock")
	if bedrockResp.Header.Get("Content-Type") == "application/vnd.amazon.eventstream" || stream {
		return e.handleStreamingResponse(bedrockResp, w)
	}
	return e.handleNonStreamingResponse(bedrockResp, w)
}

func (e *BedrockProxy) TransformChatCompletionRequest(reqBody openai_types.IncomingChatCompletionRequest) ([]byte, error) {

	// log the request as a pretty json string for debugging
	reqBodyStr, _ := json.MarshalIndent(reqBody, "", "  ")
	logrus.Debugf("Raw Request body: %s", reqBodyStr)

	bedrockRequest := bedrock.Request{
		Messages:        transformMessages(reqBody.Messages),
		InferenceConfig: buildInferenceConfig(reqBody),
		System: []bedrock.SystemMessage{
			{Text: "You are an assistant."},
		},
	}

	toolConfig := buildToolConfig(reqBody)
	if toolConfig != nil && len(toolConfig.Tools) > 0 {
		bedrockRequest.ToolConfig = toolConfig
	}

	// log the bedrock request as a pretty json string for debugging
	bedrockRequestStr, _ := json.MarshalIndent(bedrockRequest, "", "  ")
	logrus.Debugf("Bedrock request: %s", bedrockRequestStr)

	return json.Marshal(bedrockRequest)
}

func (e *BedrockProxy) handleNonStreamingResponse(bedrockResp *http.Response, w http.ResponseWriter) error {
	logrus.Infof("Sending non-streaming response back")
	defer bedrockResp.Body.Close()
	logrus.Infof("Bedrock response status: %s", bedrockResp.Status)

	var bedrockBody bedrock.Response
	if err := json.NewDecoder(bedrockResp.Body).Decode(&bedrockBody); err != nil {
		logrus.Infof("Error decoding Bedrock response: %v", err)
		return err
	}

	logrus.Debugf("Bedrock resp %v", bedrockBody)

	openAIResp := createOpenAIResponse(bedrockBody)
	return sendOpenAIResponse(openAIResp, w)
}

func (e *BedrockProxy) handleStreamingResponse(bedrockResp *http.Response, w http.ResponseWriter) error {
	logrus.Info("Sending streaming response back")
	defer bedrockResp.Body.Close()

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

	endpoint := fmt.Sprintf("%s/model/%s/%s", e.Backend.String(), model, getEndpointSuffix(stream))

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
