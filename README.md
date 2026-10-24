# llm-proxy

[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

A self-hosted, multi-protocol LLM gateway written in Go. Give every client one endpoint, route requests to many upstream providers, and translate between **OpenAI Chat Completions**, **OpenAI Responses**, and **Anthropic Messages** — including streaming — through a protocol-neutral intermediate representation.

```
                    ┌──────────────────────────────────────────┐
  OpenAI SDK ──────▶│ /api/v1/chat/completions                 │
  OpenAI Responses ─▶│ /api/v1/responses                        │──▶ IR ──▶ Router ──▶ OpenAI-compatible APIs
  Anthropic SDK ───▶│ /api/v1/messages                         │            │        Anthropic-compatible APIs
                    └──────────────────────────────────────────┘            │
                                                              weighted routing, retries, failover
```

## Why

- **One key, many providers.** Clients use a single model alias; the gateway fans out to one or more upstreams with optional weights.
- **Protocol compatibility.** Point an Anthropic SDK at an OpenAI backend (or the other way around) without changing client code.
- **Provider flexibility.** Move a model between vendors, add a cheaper fallback, or run A/B traffic with a config change.
- **Self-hosted and small.** One static binary, no database, no external control plane.

## Features

- **Three inbound wire formats:** OpenAI Chat Completions, OpenAI Responses, Anthropic Messages.
- **Protocol translation in both directions,** for requests, non-streaming responses, and SSE streams.
- **Lossless same-wire passthrough.** When client and upstream speak the same format, JSON and SSE bodies are forwarded as-is (only the public model name is rewritten), so fields the IR does not model — `logprobs`, `system_fingerprint`, `service_tier`, extra choices — are preserved.
- **Weighted routing, retries, and failover.** Per-provider retry budgets, exponential backoff, `Retry-After` support, and automatic failover to the next candidate.
- **Sticky routing.** Repeated requests with the same `x-request-id` stay on the same upstream (bounded in-memory LRU).
- **Streaming.** SSE passthrough or cross-protocol event conversion, with terminal error events instead of silently truncated streams.
- **Multimodal and tools.** Text, images, video input, tool calls/results, reasoning/thinking content, stop sequences, and Anthropic prompt-cache hints survive protocol hops.
- **Authentication and ACLs.** API-key users with optional per-user model allow-lists. Authentication is disabled when no users are configured.
- **Model catalog.** OpenAI-compatible `GET /api/v1/models` and `GET /api/v1/models/{id}`, filtered by the caller's ACL.
- **Operational niceties.** Health endpoint, one access-log line per API operation, request IDs, provider response headers, graceful shutdown, and a small multi-stage Docker image.

## Supported endpoints

| Inbound endpoint | Wire format | Can be served by upstream endpoints |
| --- | --- | --- |
| `POST /api/v1/chat/completions` | OpenAI Chat Completions | `/chat/completions`, `/responses`, `/messages` |
| `POST /api/v1/responses` | OpenAI Responses | `/responses`, `/chat/completions`, `/messages` |
| `POST /api/v1/messages` | Anthropic Messages | `/messages`, `/chat/completions`, `/responses` |
| `POST /api/v1/embeddings` | OpenAI Embeddings | `/embeddings` (opaque passthrough) |

The gateway also exposes:

| Endpoint | Description |
| --- | --- |
| `GET /api` | Gateway name, version, model count, advertised routes |
| `GET /api/health` | Unauthenticated health probe |
| `GET /api/v1/models` | OpenAI-style model list (ACL-filtered) |
| `GET /api/v1/models/{model_id}` | Model metadata (ACL-checked) |

A provider may declare several endpoints. The router prefers the endpoint that matches the inbound wire format and otherwise falls back to the first compatible chat-family endpoint, converting as needed.

| Provider `protocol` | Allowed `endpoints` | Default |
| --- | --- | --- |
| `openai` | `/chat/completions`, `/responses`, `/embeddings` | `/chat/completions` |
| `anthropic` | `/messages` | `/messages` |

## Quick start

### From source

Requires Go 1.26 or newer.

```bash
git clone https://github.com/ratsdev/llm-proxy.git
cd llm-proxy

cp config.example.toml config.toml
$EDITOR config.toml

go build -o bin/llm-proxy ./cmd/llm-proxy
./bin/llm-proxy --config config.toml
```

The gateway listens on `0.0.0.0:4000` by default. Every API route lives under `/api`, so the base URL is `http://127.0.0.1:4000/api/`; paths outside that prefix return `404`. Append `/v1` for OpenAI-style clients (for example `http://127.0.0.1:4000/api/v1`).

### Docker

```bash
docker build -t llm-proxy .

docker run --rm -p 4000:4000 \
  -v "$PWD/config.toml:/etc/llm-proxy/config.toml:ro" \
  llm-proxy
```

The image runs as an unprivileged user (`10001:10001`) and reads its config from `/etc/llm-proxy/config.toml`. Mount the file read-only; never bake credentials into the image.

### CLI flags

```
llm-proxy --config <path> [--host <addr>] [--port <port>] [--log-level <level>] [--version]

  -c, --config     path to the TOML configuration file (required)
      --host       listen address, overrides server.host
      --port       listen port, overrides server.port
      --log-level  debug | info | warning | error
      --version    print the version and exit
```

Flags take precedence over the config file.

## Configuration

`config.toml` is deliberately git-ignored because it contains provider keys and user API keys. Unknown keys are rejected at startup so a typo such as `protcol` cannot silently fall back to a default.

```toml
[general]
# Drop null values and litellm_/underscore-prefixed fields when forwarding a
# same-wire request verbatim.
drop_params = true

[server]
host = "0.0.0.0"
port = 4000
# server.log_level wins over general.log_level.
log_level = "info"

[[providers]]
id = "openai-prod"                     # unique provider id
protocol = "openai"                    # openai | anthropic
base_url = "https://api.openai.com/v1" # endpoint paths are appended
key = "sk-..."                         # Bearer token, or x-api-key for anthropic
max_retries = 2                        # 0-10, default 2
timeout = 120                          # optional request/stream idle timeout (seconds)
endpoints = ["/chat/completions", "/responses"]
# headers = { "x-custom-header" = "value" }  # applied last; can override auth headers
# proxy = "socks5://user:pass@127.0.0.1:1080" # http(s) or socks5(h), no env proxy

[[providers]]
id = "anthropic-prod"
protocol = "anthropic"
base_url = "https://api.anthropic.com/v1"
key = "sk-ant-..."
max_retries = 2
endpoints = ["/messages"]

[[models]]
id = "chat-default"                    # the name clients send in "model"
context_length = 128000               # metadata for /api/v1/models
max_output_length = 16384             # default max tokens after cross-wire conversion
modalities = ["text", "image"]        # metadata for /api/v1/models
timeout = 300                          # optional, overrides provider.timeout
providers = [
    { provider_id = "openai-prod",    model = "gpt-4.1-mini", weight = 0.7 },
    { provider_id = "anthropic-prod", model = "claude-sonnet-4", weight = 0.3 },
]

[[users]]
id = "alice"
api_key = "sk-alice"
allowed_models = ["chat-default"]     # omit to allow every model; "*" also works

[[users]]
id = "bob"
api_key = "sk-bob"
allowed_models = ["*"]
```

### Providers

| Field | Required | Description |
| --- | --- | --- |
| `id` | yes | Unique provider identifier. |
| `base_url` | yes | `http` or `https` base URL; trailing slashes are stripped. Endpoint paths are appended verbatim, so include the version prefix (`/v1`). |
| `protocol` | no | `openai` (default) or `anthropic`. Controls auth headers and which endpoints may be declared. |
| `key` | no | Sent as `Authorization: Bearer <key>` for OpenAI, `x-api-key: <key>` for Anthropic. |
| `max_retries` | no | Retries per request on transient failures, `0`-`10`, default `2`. |
| `timeout` | no | Request and stream-idle timeout in seconds. Default `600`. |
| `endpoints` | no | Upstream paths this provider implements. Defaults to the protocol's default endpoint. Duplicates are ignored. |
| `headers` | no | Extra headers applied last. They can override `Authorization`, `Accept`, and other defaults. |
| `proxy` | no | HTTP(S) or SOCKS5 proxy for this provider. Environment proxy variables are never consulted. |

### Models

| Field | Required | Description |
| --- | --- | --- |
| `id` | yes | Client-visible model alias. It may contain slashes. |
| `providers` | yes | Non-empty list of `{ provider_id, model, weight }`. `model` is the upstream model name. |
| `weight` | no | Positive routing weight. Declare weights for all providers or none; declared weights must sum to `1.0`. |
| `context_length` | no | Metadata returned by the models API. |
| `max_output_length` | no | Default output token budget when a request is converted across formats and does not specify one. |
| `modalities` | no | Metadata returned by the models API. Defaults to `["text"]`. |
| `timeout` | no | Overrides the provider timeout for this model. |

If no weights are declared, every provider gets weight `1.0` (equal selection probability).

### Users

| Field | Required | Description |
| --- | --- | --- |
| `id` | yes | Unique user identifier, used in logs. |
| `api_key` | yes | Bearer token or `x-api-key` value. Must be unique. |
| `allowed_models` | no | Model allow-list. Omit for unrestricted access; `"*"` is a wildcard. |

**Authentication is disabled entirely when there are no `[[users]]`.** Do not expose an unauthenticated instance to the internet.

## Usage

### OpenAI Chat Completions

```bash
curl http://127.0.0.1:4000/api/v1/chat/completions \
  -H "Authorization: Bearer sk-alice" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "chat-default",
    "messages": [{"role": "user", "content": "Explain SSE in one sentence."}],
    "stream": true
  }'
```

### OpenAI Responses

```bash
curl http://127.0.0.1:4000/api/v1/responses \
  -H "Authorization: Bearer sk-alice" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "chat-default",
    "input": "Explain SSE in one sentence."
  }'
```

### Anthropic Messages

```bash
curl http://127.0.0.1:4000/api/v1/messages \
  -H "x-api-key: sk-alice" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "chat-default",
    "max_tokens": 512,
    "messages": [{"role": "user", "content": "Explain SSE in one sentence."}]
  }'
```

### Models

```bash
curl http://127.0.0.1:4000/api/v1/models -H "Authorization: Bearer sk-alice"
curl http://127.0.0.1:4000/api/v1/models/chat-default -H "Authorization: Bearer sk-alice"
```

### SDK example

Any OpenAI-compatible SDK works by changing the base URL:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:4000/api/v1",
    api_key="sk-alice",
)

response = client.chat.completions.create(
    model="chat-default",
    messages=[{"role": "user", "content": "Hello"}],
)
print(response.choices[0].message.content)
```

An Anthropic SDK can point at the same gateway:

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://127.0.0.1:4000/api",
    api_key="sk-alice",
)

message = client.messages.create(
    model="chat-default",
    max_tokens=256,
    messages=[{"role": "user", "content": "Hello"}],
)
print(message.content[0].text)
```

## Routing and reliability

For each request the gateway:

1. Authenticates the caller and resolves the model alias.
2. Builds candidates from the model's provider list, dropping providers that cannot serve the operation or the content (for example, video requests never go to Anthropic-protocol providers).
3. Orders candidates by weight, unless a sticky entry for the request ID exists.
4. Sends the request to each candidate in order, retrying transient failures up to that provider's `max_retries`.
5. Fails over to the next provider on retryable errors.

Retryable upstream statuses: `408`, `409`, `425`, `429`, `500`, `502`, `503`, `504`, `529` (Anthropic overloaded). Other `4xx` responses are returned to the client immediately. Backoff starts at 250 ms, doubles, and is capped at 2 s; `Retry-After` is honored up to 5 s.

**Sticky routing:** if a client sends a valid `x-request-id` (up to 128 characters of `A-Z a-z 0-9 . _ : -`), a successful upstream is remembered for that ID and preferred by later requests with the same ID. The cache is in-memory, bounded to 10,000 entries, and is not shared between replicas. If no `x-request-id` is supplied, the gateway generates one for the response but does not pin the request.

**Response headers:**

| Header | Meaning |
| --- | --- |
| `x-request-id` | Client-supplied or generated request ID. |
| `x-provider-id` | Upstream provider that served the request. |
| `x-upstream-model` | Upstream model name that was called. |
| `Retry-After` | Forwarded from the upstream on errors. |

## Protocol translation

Same-wire requests are forwarded as raw JSON after `model` (and, with `drop_params`, null/`litellm_`/`_` fields) is handled; same-wire responses and SSE streams are passed through with only the model name rewritten. This preserves vendor-specific fields and is the preferred path.

Cross-wire requests are parsed into the IR and re-encoded for the selected upstream:

- text, image, and video content blocks;
- tool definitions, tool calls, and tool results;
- reasoning / thinking blocks and signatures;
- usage totals, cached-input tokens, and reasoning tokens;
- stop reasons and stop sequences;
- Anthropic `cache_control` hints when the upstream is Anthropic.

Some fields do not have an equivalent in every format and are dropped on a cross-wire hop. Embeddings are never translated: they are proxied opaquely to a provider that declares `/embeddings`.

Streaming behavior:

- **Same wire:** SSE bytes are forwarded as they arrive, with the `model` field rewritten in place.
- **Different wire:** SSE events are decoded into IR events and re-encoded for the client's protocol.
- **Upstream ignored `stream`:** a complete JSON reply is converted into a synthetic SSE stream.
- **Interrupted stream:** the client receives the protocol's terminal error event rather than a silent truncation.

## Operations

- **Health:** `GET /api/health` is unauthenticated and returns model/provider counts.
- **Index:** `GET /api` returns the version and advertised endpoints.
- **Errors:** protocol-native JSON envelopes. Request failures include a stable `code` such as `model_not_found`, `model_not_allowed`, `invalid_json`, or `upstream_error`.
- **Request size:** request bodies are limited to 64 MiB.
- **Response size:** buffered upstream replies are limited to 64 MiB; a single cross-protocol SSE event is limited to 8 MiB of data.
- **Logging:** one structured access-log line per API operation, including request ID, user, model, provider, stream flag, status, duration, and token usage when known. Health and index probes are not logged.
- **Timeouts:** model timeout overrides provider timeout, which defaults to 600 seconds. The timeout applies to the request and to stream inactivity.
- **Shutdown:** `SIGINT`/`SIGTERM` triggers a graceful shutdown with a 30-second deadline.
- **Reloading:** configuration is loaded once at startup. Restart the process to apply changes.
- **TLS:** the gateway speaks plain HTTP. Terminate TLS in a reverse proxy or ingress.

## Architecture

Every request is parsed into a small protocol-neutral IR (`ir/`), routed to a provider, and re-encoded. Adding a wire format means adding one adapter rather than pairwise converters.

| Path | Purpose |
| --- | --- |
| `cmd/llm-proxy/` | CLI entry point, flags, startup, graceful shutdown |
| `server.go` | HTTP routes, auth, model API, access logging, error envelopes |
| `proxy.go` | Inbound request handling and response/SSE relay |
| `router.go` | Candidate selection, weighted order, retries, failover, sticky cache |
| `wire.go` | Single source of truth for inbound routes and provider endpoints |
| `config.go` | TOML loading and validation |
| `client.go` | Upstream HTTP clients, timeouts, HTTP/SOCKS5 proxies |
| `ir/` | Protocol-neutral request, response, content block, and stream event types |
| `protocols/` | Adapter contract plus OpenAI and Anthropic implementations |
| `util/` | JSON helpers, SSE parser, error envelope, IDs |

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
gofmt -l .
```

Build with a version string:

```bash
go build -trimpath \
  -ldflags "-s -w -X github.com/ratsdev/llm-proxy.Version=1.0.0" \
  -o bin/llm-proxy ./cmd/llm-proxy
```

## Limitations

- No embeddings translation; embeddings only pass through to `/embeddings` upstreams.
- Anthropic-protocol providers cannot receive video content.
- Cross-wire conversion covers the common chat surface, not every vendor-specific extension.
- No per-user rate limiting, quotas, cost accounting, admin UI, or config hot reload.
- Sticky routing is per-process and in-memory.
- No built-in TLS termination.

## Security

- Provider keys and user API keys are stored in plaintext in `config.toml`. Keep the file mode `0600`, mount it read-only in containers, and never commit it.
- With no `[[users]]`, anyone who can reach the port can use every model. Put the gateway behind a private network, VPN, or authenticating reverse proxy.
- Treat configured provider headers as privileged: they are applied after authentication headers and can override them.

## Contributing

Issues and pull requests are welcome. Please run the test suite and `gofmt` before opening a PR, and include tests for protocol-conversion or routing changes.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full text.
