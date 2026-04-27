# goop

A small Go reverse proxy that fronts every LLM provider behind one URL and one
bearer token. Point your existing OpenAI / boto3 / google-genai / anthropic
SDK at `goop`; `goop` injects the upstream credential and forwards bytes
unchanged.

```
              ┌────────────────────┐
client ──────►│   goop (Bearer)    │──── upstream auth (SigV4 / x-api-key / …) ───►  LLM
              └────────────────────┘
```

Use it when you want one place to hold cloud credentials, swap providers by
changing a `base_url`, or run a tiny aggregator in your homelab without
adopting a full LLM gateway.

## Routes

### Native passthrough — clients keep using each provider's own SDK

| Prefix       | Upstream                                          | Auth `goop` injects                     |
| ------------ | ------------------------------------------------- | --------------------------------------- |
| `/openai`    | `https://api.openai.com`                          | `Authorization: Bearer …`               |
| `/azure`     | your Azure OpenAI resource                        | `api-key: …`                            |
| `/together`  | `https://api.together.xyz`                        | `Authorization: Bearer …`               |
| `/bedrock`   | `https://bedrock-runtime.{region}.amazonaws.com`  | AWS SigV4                               |
| `/gemini`    | `https://generativelanguage.googleapis.com`       | `X-Goog-Api-Key: …`                     |
| `/anthropic` | `https://api.anthropic.com`                       | `x-api-key: …` + `anthropic-version: …` |

### OpenAI-compatible passthrough — point the OpenAI SDK at any of these

| Prefix              | Provider's OpenAI-compat endpoint                       |
| ------------------- | ------------------------------------------------------- |
| `/openai`           | OpenAI native (already OpenAI-shaped)                   |
| `/azure`            | Azure OpenAI v1                                         |
| `/together`         | Together AI                                             |
| `/gemini-openai`    | Google `generativelanguage` `/v1beta/openai`            |
| `/anthropic-openai` | Anthropic's OpenAI-compat layer                         |
| `/bedrock-openai`   | AWS Bedrock Mantle (SigV4-signed, OpenAI-shaped)        |

### Translator — Bedrock models that Mantle doesn't yet cover

| Endpoint                                      | Notes                                                                |
| --------------------------------------------- | -------------------------------------------------------------------- |
| `POST /bedrock-translate/v1/chat/completions` | OpenAI → Bedrock Converse for Claude, Llama, etc.                    |
| `POST /openai-bedrock/v1/chat/completions`    | Legacy alias for the same handler.                                   |

The translator is text-only. Multimodal requests get a 400 pointing at
`/bedrock-openai/v1` (Mantle), which handles images natively.

### Catalog & probes

| Endpoint         | Behavior                                                                  |
| ---------------- | ------------------------------------------------------------------------- |
| `GET /v1/models` | Unions every enabled provider's model list, TTL-cached (10 min default).  |
| `GET /healthz`   | Liveness — always 200, runs outside the auth middleware.                  |
| `GET /ready`     | Readiness — 200 once at least one provider is configured, else 503.       |

## Quickstart

```bash
cp .env.example .env       # fill in any subset of provider keys
make run                   # binary on :8080
# or
docker compose up --build
```

Smoke test:

```bash
curl -s http://localhost:8080/v1/models | jq '.data | length'
```

OpenAI Python SDK against any compat endpoint:

```python
from openai import OpenAI

oa = OpenAI(api_key="goop-bearer", base_url="http://localhost:8080/openai/v1")
tg = OpenAI(api_key="goop-bearer", base_url="http://localhost:8080/together/v1")
bm = OpenAI(api_key="goop-bearer", base_url="http://localhost:8080/bedrock-openai/v1")
ba = OpenAI(api_key="goop-bearer", base_url="http://localhost:8080/bedrock-translate/v1")
```

Native Bedrock with boto3 (SigV4 happens server-side):

```python
import boto3
client = boto3.client("bedrock-runtime", endpoint_url="http://localhost:8080/bedrock")
```

## Configuration

`config.yml` is the source of truth. `${VAR}` and `${VAR:-default}` are
substituted from the environment before parse, then YAML is decoded with
strict mode (unknown keys cause startup to fail). Providers with missing
required secrets are silently skipped — check the startup log to see which
ones came up.

To add another OpenAI-compatible provider, copy any `openai-compat` block in
`config.yml` and change `base_url` + `api_key`. No code change needed.

## Layout

```
cmd/goop/             entrypoint
internal/
  auth/               single shared bearer middleware (constant-time compare)
  config/             yaml + env loader (strict mode)
  models/             /v1/models aggregator with TTL cache
  provider/           Provider interface + implementations
                      (openai-compat, bedrock-native, gemini, anthropic, sigv4)
  proxy/              shared transport + per-provider httputil.ReverseProxy
  translator/         OpenAI ↔ Bedrock Converse handler
```

## Design notes

- **Streaming**: SSE and Bedrock event-streams pass through with no buffering.
  `httputil.ReverseProxy` runs with `FlushInterval: -1`, and `Content-Length`
  is stripped on event-stream responses so middlebox layers don't buffer.
- **SigV4**: Bedrock SigV4 happens in a custom `RoundTripper`. The request
  body is buffered just long enough to compute its SHA256; the response is
  never buffered.
- **Dynamic models**: `/v1/models` calls each provider's listing API on
  demand (TTL-cached, deduped by ID, first-seen wins). New models appear
  automatically.
- **Header hygiene**: every provider's `Rewrite` strips client-supplied
  auth headers (`Authorization`, `Api-Key`, `X-Api-Key`, `X-Goog-Api-Key`,
  `Anthropic-Version`, `Proxy-Authorization`) before injecting its own —
  prevents accidental credential smuggling.
- **Body cap**: passthrough requests are wrapped with a 10 MiB
  `MaxBytesReader` so an unbounded streamed POST can't OOM the process or
  the SigV4 SHA256 buffer. Oversize requests get 413.
- **No request rewriting on passthrough**: provider-native routes are
  byte-for-byte transparent. Only the translator endpoint parses request
  bodies.

## Scope (and non-scope)

`goop` is a homelab/single-tenant proxy. It is intentionally _not_:

- a per-user quota / billing layer
- a request/response logger or audit store
- a multi-tenant key vault

If you need any of that, you want
[LiteLLM](https://github.com/BerriAI/litellm), [Portkey](https://portkey.ai),
or an internal gateway. `goop` aims to be the thing you reach for when those
feel like overkill.

## Development

```bash
make fmt vet test       # standard loop
make lint               # golangci-lint (config in .golangci.yml)
go test ./... -race     # race detector
```

Tests are hermetic — they do not call out to real providers. SigV4 signing,
the translator, the model aggregator, and the proxy are each covered by unit
tests with `httptest.Server` upstreams.
