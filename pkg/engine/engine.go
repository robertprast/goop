package engine

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	openai_types "github.com/robertprast/goop/pkg/openai_proxy/types"
)

type Engine interface {
	Name() string
	IsAllowedPath(path string) bool
	ModifyRequest(r *http.Request)
	HandleResponseAfterFinish(resp *http.Response, body []byte)
}

type OpenAIProxyEngine interface {
	Engine
	TransformChatCompletionRequest(reqBody openai_types.InconcomingChatCompletionRequest) ([]byte, error)
	HandleChatCompletionRequest(ctx context.Context, transformedBody []byte, stream bool) (*http.Response, error)
	SendChatCompletionResponse(bedrockResp *http.Response, w http.ResponseWriter, stream bool) error
}

type contextKey string
type requestIdKey string

const (
	engineKey = contextKey("engine")
	RequestId = requestIdKey("requestId")
)

func ContextWithEngine(ctx context.Context, eng Engine) context.Context {
	ctx = context.WithValue(ctx, RequestId, uuid.New().String())
	return context.WithValue(ctx, engineKey, eng)
}

func FromContext(ctx context.Context) Engine {
	eng, _ := ctx.Value(engineKey).(Engine)
	return eng
}
