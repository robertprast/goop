package engine

import (
	"context"
	"github.com/robertprast/goop/pkg/openai_schema"
	"io"
	"net/http"

	"github.com/google/uuid"
)

type Engine interface {
	Name() string
	IsAllowedPath(path string) bool
	ModifyRequest(r *http.Request)
	ResponseCallback(resp *http.Response, body io.Reader)
	ListModels() ([]openai_schema.Model, error)
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
