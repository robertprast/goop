package proxy

import (
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/robertprast/goop/pkg/audit"
	"github.com/robertprast/goop/pkg/engine"
	azure "github.com/robertprast/goop/pkg/engine/azure_openai"
	"github.com/robertprast/goop/pkg/engine/bedrock"
	"github.com/robertprast/goop/pkg/engine/openai"
	"github.com/robertprast/goop/pkg/engine/vertex"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

type middleware func(http.Handler) http.Handler

func NewProxyHandler(config utils.Config) http.Handler {
	var handler http.Handler = http.HandlerFunc(reverseProxy)
	handler = chainMiddlewares(handler, auditMiddleware, engineMiddleware(config), logMiddleware)
	return handler
}

func chainMiddlewares(finalHandler http.Handler, middlewares ...middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		finalHandler = middlewares[i](finalHandler)
	}
	return finalHandler
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(engine.RequestId).(string)
		logrus.Infof("Request Correlation ID: %s %s", id, r.Method)
		next.ServeHTTP(w, r)
	})
}

func auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logrus.Infof("Auditing request: %s %s", r.Method, r.URL.Path)
		bodyCopy, err := audit.CopyRequestBody(r)
		if err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		go audit.AuditRequest(r, bodyCopy)
		next.ServeHTTP(w, r)
	})
}

func engineMiddleware(config utils.Config) middleware {
	return func(next http.Handler) http.Handler {

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			firstPathSegment := strings.Split(r.URL.Path, "/")[1]
			logrus.Infof("First path segment: %s", firstPathSegment)

			var eng engine.Engine
			var err error
			switch firstPathSegment {
			case "openai":
				eng, err = openai.NewOpenAIEngineWithConfig(config.Engines["openai"])
			case "azure":
				eng, err = azure.NewAzureOpenAIEngineWithConfig(config.Engines["azure"])
			case "bedrock":
				eng, err = bedrock.NewBedrockEngine(config.Engines["bedrock"])
			case "vertex":
				eng, err = vertex.NewVertexEngine(config.Engines["vertex"])
			}

			if err != nil {
				logrus.Errorf("Error selecting engine: %v", err)
				http.Error(w, "Error selecting engine", http.StatusNotFound)
				return
			}

			if !eng.IsAllowedPath(strings.TrimPrefix(r.URL.Path, eng.Name())) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			ctx := engine.ContextWithEngine(r.Context(), eng)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func reverseProxy(w http.ResponseWriter, r *http.Request) {
	eng := engine.FromContext(r.Context())
	if eng == nil {
		http.Error(w, "Engine not found", http.StatusInternalServerError)
		return
	}

	eng.ModifyRequest(r)

	proxy := &httputil.ReverseProxy{
		Director:       func(req *http.Request) {},
		ModifyResponse: audit.AuditResponse,
		Transport:      http.DefaultTransport,
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	proxy.ServeHTTP(w, r)
	flusher.Flush()
}
