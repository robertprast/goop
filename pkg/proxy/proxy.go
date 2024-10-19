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
	"github.com/sirupsen/logrus"
)

type middleware func(http.Handler) http.Handler

func NewProxyHandler() http.Handler {
	var handler http.Handler = http.HandlerFunc(reverseProxy)
	handler = chainMiddlewares(handler, auditMiddleware, engineMiddleware, logMiddleware)
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

func engineMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engines := map[string]func() engine.Engine{
			"/openai":  func() engine.Engine { return openai.NewOpenAIEngine() },
			"/bedrock": func() engine.Engine { return bedrock.NewBedrockEngine() },
			"/azure":   func() engine.Engine { return azure.NewAzureOpenAIEngine() },
			"/vertex":  func() engine.Engine { return vertex.NewVertexEngine() },
		}

		var eng engine.Engine
		var found bool
		for prefix, constructor := range engines {
			if strings.HasPrefix(r.URL.Path, prefix) {
				logrus.Infof("Choosing engine: %s", prefix)
				eng = constructor()
				found = true
				break
			}
		}

		if !found || !eng.IsAllowedPath(strings.TrimPrefix(r.URL.Path, eng.Name())) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		ctx := engine.ContextWithEngine(r.Context(), eng)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
