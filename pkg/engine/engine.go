package engine

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type Engine interface {
	Name() string
	IsAllowedPath(path string) bool
	ModifyRequest(r *http.Request)
	HandleResponseAfterFinish(resp *http.Response, body []byte)
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
