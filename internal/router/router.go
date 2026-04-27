// Package router implements goop's top-level /v1/chat/completions handler:
// a single OpenAI-shaped endpoint that inspects the request body's "model"
// field and dispatches to the right provider's chat-completions path.
//
// Without it, every client has to know goop's per-provider URL prefixes:
//
//	/openai/v1/chat/completions       → OpenAI
//	/together/v1/chat/completions     → Together
//	/bedrock-openai/v1/chat/completions     → Bedrock Mantle (gpt-oss)
//	/bedrock-translate/v1/chat/completions  → Bedrock Converse (Anthropic, etc.)
//
// With it, clients send `{"model": "together/moonshotai/Kimi-K2.6", ...}` to
// /v1/chat/completions and goop figures out where it belongs. This is what
// LiteLLM was doing in its model_list, but goop already has the catalog —
// surfacing the same routing inside goop removes the need for a static list.
package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/robertprast/goop/internal/provider"
)

// maxBodyBytes mirrors the proxy/translator caps so a streamed POST can't
// exhaust memory while we're parsing the model field.
const maxBodyBytes = 10 << 20 // 10 MiB

// Resolver classifies a model ID into a forward target.
//
// Three flavors of target:
//
//   - Passthrough: rewrite the URL path to "<prefix>/v1/chat/completions"
//     and let the existing reverse-proxy infrastructure handle it. Used
//     for every openai-compat provider plus Bedrock Mantle.
//
//   - Translator: invoke a registered http.Handler directly (no path
//     rewrite). Used for Bedrock Converse — the translator handler reads
//     the body itself and emits Converse stream events back to the caller.
type Resolver struct {
	registry      *provider.Registry
	bedrockTrans  http.Handler
	logger        *slog.Logger
}

// NewResolver builds a Resolver. bedrockTranslator may be nil if the
// translator isn't wired up; in that case bedrock/<non-Mantle> models will
// 400 instead of routing.
func NewResolver(reg *provider.Registry, bedrockTranslator http.Handler, logger *slog.Logger) *Resolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{registry: reg, bedrockTrans: bedrockTranslator, logger: logger}
}

// Decision is the resolved routing for a single request.
type Decision struct {
	// PassthroughPrefix, if non-empty, means dispatch to
	// <PassthroughPrefix>/v1/chat/completions through the existing proxy.
	PassthroughPrefix string

	// TranslatorHandler, if non-nil, means call this handler directly
	// (PassthroughPrefix and StrippedModel are ignored — translators read
	// the body themselves and unwrap any goop namespace prefix).
	TranslatorHandler http.Handler

	// StrippedModel is the upstream-facing model ID after removing goop's
	// "<provider>/" namespace prefix. The router rewrites the request body
	// to use this before forwarding.
	StrippedModel string

	// Reason is a human-readable description of how this decision was made,
	// used in the 400 error body if Resolve fails to match anything.
	Reason string
}

// Resolve picks a route for the given model ID. Returns a Decision plus an
// error suitable for direct rendering back to the caller.
func (r *Resolver) Resolve(modelID string) (Decision, error) {
	if modelID == "" {
		return Decision{}, fmt.Errorf("missing required field \"model\"")
	}

	// Bedrock has two egress paths sharing one "bedrock/" namespace, so it
	// has to be split before the generic prefix-match.
	if rest, ok := strings.CutPrefix(modelID, "bedrock/"); ok {
		// AWS Mantle's OpenAI-compat endpoint serves the openai.* family
		// (gpt-oss-20b/120b today). Everything else under bedrock/ —
		// Anthropic, Nova, Llama, Mistral, etc. — has to go through the
		// Converse translator.
		if isMantleModel(rest) {
			if p := r.registry.ByName("bedrock-openai"); p != nil {
				return Decision{
					PassthroughPrefix: p.Prefix(),
					StrippedModel:     rest,
					Reason:            "bedrock/openai.* → bedrock-openai (Mantle)",
				}, nil
			}
			return Decision{}, fmt.Errorf("model %q matches Bedrock Mantle but the bedrock-openai provider is not configured", modelID)
		}
		if r.bedrockTrans != nil {
			return Decision{
				TranslatorHandler: r.bedrockTrans,
				// Translator strips "bedrock/" itself; pass through.
				StrippedModel: modelID,
				Reason:        "bedrock/* → bedrock-translate (Converse)",
			}, nil
		}
		return Decision{}, fmt.Errorf("model %q requires the Bedrock Converse translator, which is not configured", modelID)
	}

	// Other providers: model IDs come back from /v1/models as
	// "<Name()>/<upstream-id>". Split on the first slash and look up the
	// owning provider. Each openai-compat provider serves chat-completions
	// at "<Prefix()>/v1/chat/completions".
	ns, rest, ok := strings.Cut(modelID, "/")
	if !ok || rest == "" {
		return Decision{}, fmt.Errorf("model %q has no goop namespace prefix; expected \"<provider>/<id>\"", modelID)
	}
	p := r.registry.ByName(ns)
	if p == nil {
		return Decision{}, fmt.Errorf("model %q references unknown provider %q", modelID, ns)
	}
	return Decision{
		PassthroughPrefix: p.Prefix(),
		StrippedModel:     rest,
		Reason:            fmt.Sprintf("%s/* → provider %q", ns, ns),
	}, nil
}

// isMantleModel reports whether a model ID (already stripped of "bedrock/")
// is served by AWS Bedrock Mantle's OpenAI-compat endpoint. Today that's
// only the openai.gpt-oss-* family, but new families AWS adds to Mantle
// (e.g. openai.gpt-oss-* successors) will land under "openai." too.
func isMantleModel(rest string) bool {
	return strings.HasPrefix(rest, "openai.")
}

// Handler returns the http.Handler for /v1/chat/completions.
//
// internalProxy is the http.Handler that knows how to dispatch to providers
// by URL path (typically the proxy.Server's Handler). The router rewrites
// the URL and body, then hands off to it.
func (r *Resolver) Handler(internalProxy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.serve(w, req, internalProxy)
	})
}

func (r *Resolver) serve(w http.ResponseWriter, req *http.Request, internalProxy http.Handler) {
	if req.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"only POST is supported on /v1/chat/completions")
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read_body", err.Error())
		return
	}
	_ = req.Body.Close()

	// Parse just enough to extract the model field. Round-trip through a
	// generic map so we can rewrite "model" without disturbing other keys
	// or losing fields the SDK doesn't know about.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		writeJSONError(w, http.StatusBadRequest, "parse_body", "request body is not valid JSON: "+err.Error())
		return
	}
	rawModel, ok := generic["model"]
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "missing_model", "request body is missing required field \"model\"")
		return
	}
	var modelID string
	if err := json.Unmarshal(rawModel, &modelID); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_model", "field \"model\" must be a string")
		return
	}

	decision, err := r.Resolve(modelID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "unroutable_model", err.Error())
		return
	}
	r.logger.Debug("router decision",
		"model", modelID,
		"target", decision.PassthroughPrefix,
		"translator", decision.TranslatorHandler != nil,
		"stripped", decision.StrippedModel,
		"reason", decision.Reason,
	)

	// For passthrough targets we rewrite "model" to the upstream-facing
	// form. For the translator we leave it alone — translator unwraps
	// "bedrock/" itself and rejects un-prefixed inputs.
	forwardBody := body
	if decision.TranslatorHandler == nil && decision.StrippedModel != modelID {
		stripped, err := json.Marshal(decision.StrippedModel)
		if err != nil {
			// Should be impossible; json.Marshal of a string can't fail.
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		generic["model"] = stripped
		forwardBody, err = json.Marshal(generic)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}

	// Rebuild the request with the (possibly rewritten) body. Content-Length
	// must match the new body length or the upstream will hang on read.
	req.Body = io.NopCloser(bytes.NewReader(forwardBody))
	req.ContentLength = int64(len(forwardBody))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(forwardBody)))
	// Drop any TransferEncoding the client set (e.g. "chunked"); we have a
	// known length now and Go's http.Server will pick the right framing.
	req.TransferEncoding = nil

	if decision.TranslatorHandler != nil {
		decision.TranslatorHandler.ServeHTTP(w, req)
		return
	}
	// Rewrite path so the existing proxy handler picks the right provider.
	req.URL.Path = decision.PassthroughPrefix + "/v1/chat/completions"
	req.URL.RawPath = ""
	internalProxy.ServeHTTP(w, req)
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    code,
			"message": msg,
		},
	})
}
