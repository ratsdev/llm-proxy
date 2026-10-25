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
- **Read-only Web UI.** A built-in console at `/ui` for models, providers, usage, and recent requests. It is unlocked with an API key and can never modify configuration.
- **Operational niceties.** Health endpoint, one access-log line per API operation, request IDs, provider response headers, graceful shutdown, and a small multi-stage Docker image.

## Supported endpoints

| Inbound endpoint | Wire format | Can be served by upstream endpoints |
| --- | --- | --- |
| `POST /api/v1/chat/completions` | OpenAI Chat Completions | `/chat/completions`, `/responses`, `/v1/messages` |
| `POST /api/v1/responses` | OpenAI Responses | `/responses`, `/chat/completions`, `/v1/messages` |
| `POST /api/v1/messages` | Anthropic Messages | `/v1/messages`, `/chat/completions`, `/responses` |

The gateway also exposes:

| Endpoint | Description |
| --- | --- |
| `GET /api` | Gateway name, version, model count, advertised routes |
| `GET /api/health` | Unauthenticated health probe |
| `GET /api/v1/models` | OpenAI-style model list (ACL-filtered) |
| `GET /api/v1/models/{model_id}` | Model metadata (ACL-checked) |
| `GET /ui` | Read-only Web UI, unlocked with an API key |

A provider may declare several endpoints. The router prefers the endpoint that matches the inbound wire format and otherwise falls back to the first compatible endpoint, converting as needed.

Omitting `endpoints` enables every default endpoint for each configured protocol. Declaring it switches that protocol to an allow-list: only the listed endpoints are enabled, and an empty path uses the default path. Protocols left out of the `endpoints` table keep every default endpoint. A custom path must be a URL path: it starts with `/` and has no scheme, host, query, fragment, or whitespace.

| Protocol | Endpoint name | Default path | Enabled when the protocol is not listed under `endpoints` |
| --- | --- | --- | --- |
| `openai` | `chat_completions` | `/chat/completions` | yes |
| `openai` | `responses` | `/responses` | yes |
| `anthropic` | `messages` | `/v1/messages` | yes |

A provider may declare base URLs and endpoints for both protocols at once.

## Quick start

### From source

Requires Go 1.26 or newer.

```bash
git clone https://github.com/ratsdev/llm-proxy.git
cd llm-proxy

cp config.example.yaml config.yaml
$EDITOR config.yaml

go build -o bin/llm-proxy ./cmd/llm-proxy
./bin/llm-proxy --config config.yaml
```

The gateway listens on `0.0.0.0:4000` by default. Every API route lives under `/api`, so the base URL is `http://127.0.0.1:4000/api/`; unknown paths outside the UI and API prefixes return `404`. Append `/v1` for OpenAI-style clients (for example `http://127.0.0.1:4000/api/v1`). The Web UI is at `http://127.0.0.1:4000/ui` and signs in with any configured user `api_key`.

### Docker

```bash
docker run --rm -p 4000:4000 \
  -v "$PWD/config.yaml:/etc/llm-proxy/config.yaml:ro" \
  ghcr.io/ratsdev/llm-proxy:latest
```

To build the image yourself instead, run `docker build -t llm-proxy .` and substitute `llm-proxy` for the image name above.

Images are published to `ghcr.io/ratsdev/llm-proxy` and to Docker Hub as `docker.io/jqjiang/llm-proxy`; the two registries carry identical tags.

| Tag | Meaning |
| --- | --- |
| `latest` | Most recently published build, including pre-releases. |
| `stable` | Most recent stable release. Pre-release tags never move it. |
| `1.2.3` | Exact release. |
| `1.2` | Latest patch of that minor line. |
| `1` | Latest release of that major line. |

Use `stable` for production and pin an exact version when reproducibility matters; `latest` intentionally tracks the newest build and may include release candidates.

The image reads its config from `/etc/llm-proxy/config.yaml` and runs as `root`, which lets it read a mounted file that is owned by root and kept at mode `0600`. Mount the file read-only; never bake credentials into the image. If you prefer an unprivileged container, start it with `--user` and make the mounted config readable by that UID.

### CLI flags

```
llm-proxy --config <path> [--host <addr>] [--port <port>] [--log-level <level>] [--version]

  -c, --config     path to the YAML configuration file (required)
      --host       listen address, overrides server.host
      --port       listen port, overrides server.port
      --log-level  debug | info | warning | error
      --version    print the version and exit
```

Flags take precedence over the config file.

## Configuration

`config.yaml` is deliberately git-ignored because it contains provider keys and user API keys. It must contain a single YAML document and must not be empty. Unknown keys are rejected at startup so a typo such as `protcol` cannot silently fall back to a default. Provider, model, and user IDs are the map keys themselves and may not have leading or trailing whitespace.

```yaml
general:
  # Drop null values and litellm_/underscore-prefixed fields when forwarding a
  # same-wire request verbatim.
  drop_params: true

server:
  host: "0.0.0.0"
  port: 4000
  # server.log_level wins over general.log_level.
  log_level: "info"

providers:
  openai-prod:
    name: "OpenAI production"
    base_url:
      openai: "https://api.openai.com/v1" # endpoint paths are appended
    api_key: "sk-..."                     # Bearer token, or x-api-key for anthropic
    max_retries: 2                        # 0-10, default 2
    timeout: 120                          # optional request/stream idle timeout (seconds)
    # endpoints is omitted: every default endpoint for the openai protocol is
    # enabled. Declare endpoints to restrict the set; an empty path means the
    # default path.
    # headers:                            # applied last; can override auth headers
    #   x-custom-header: "value"
    # proxy: "socks5://user:pass@127.0.0.1:1080" # http(s) or socks5(h), no env proxy

  anthropic-prod:
    name: "Anthropic production"
    base_url:
      anthropic: "https://api.anthropic.com" # messages defaults to /v1/messages
    api_key: "sk-ant-..."

  # One provider can expose both protocol base URLs and endpoint tables.
  dual-provider:
    base_url:
      openai: "https://gateway.example.com/v1"
      anthropic: "https://gateway.example.com"
    api_key: "sk-dual"
    # Declaring endpoints enables only these two; the empty values use the
    # default paths (/chat/completions and /v1/messages).
    endpoints:
      openai:
        chat_completions:
      anthropic:
        messages:

models:
  chat-default:                           # the name clients send in "model"
    name: "Chat default"
    context_length: 128000                # metadata for /api/v1/models
    max_output_length: 16384              # default max tokens after cross-wire conversion
    modalities:
      input: ["text", "image"]            # metadata for /api/v1/models
      output: ["text"]
    timeout: 300                           # optional, overrides provider.timeout
    providers:
      - provider_id: "openai-prod"
        model_id: "gpt-4.1-mini"
        weight: 0.7
        max_retry_after: 10               # optional Retry-After cap for this binding (seconds)
      - provider_id: "anthropic-prod"
        model_id: "claude-sonnet-4"
        weight: 0.3

users:
  alice:
    name: "Alice"
    api_key: "sk-alice"
    allowed_models: ["chat-default"]     # omit to allow every model; "*" also works

  bob:
    name: "Bob"
    api_key: "sk-bob"
    allowed_models: ["*"]
```

### Providers

| Field | Required | Description |
| --- | --- | --- |
| `base_url` | yes | Mapping keyed by protocol (`openai`, `anthropic`). At least one protocol is required. Each value must be an `http` or `https` base URL; trailing slashes are stripped. Endpoint paths are appended verbatim, so include any version prefix. The default Anthropic messages path is `/v1/messages`, so an Anthropic base URL normally omits `/v1` unless you override the path. |
| `api_key` | no | Sent as `Authorization: Bearer <key>` for OpenAI endpoints, `x-api-key: <key>` for Anthropic endpoints. |
| `name` | no | Human-readable label shown in the Web UI; the provider ID is shown when empty. |
| `max_retries` | no | Retries per request on transient failures, `0`-`10`, default `2`. |
| `timeout` | no | Request and stream-idle timeout in seconds. Default `600`. |
| `endpoints` | no | Optional per-protocol allow-list mapping protocol to endpoint name to upstream path. When omitted, every default endpoint for each configured protocol is enabled. When present, a listed protocol keeps only its listed endpoints; a missing or empty path uses the default path, and protocols left out keep every default endpoint. Endpoint names are `chat_completions`, `responses`, and `messages`. Custom paths must start with `/` and may not include a scheme, host, query, fragment, or whitespace. |
| `headers` | no | Extra headers applied last. They can override `Authorization`, `Accept`, and other defaults. |
| `proxy` | no | HTTP(S) or SOCKS5 proxy for this provider. Environment proxy variables are never consulted. |

The map key is the provider ID. When routing a chat request, the preferred protocol matches the inbound wire format: OpenAI-style requests use the provider's `openai` endpoints, and Anthropic-style requests use its `anthropic` endpoints. If the preferred protocol is unavailable, the router falls back to another compatible endpoint and translates the request.

### Models

| Field | Required | Description |
| --- | --- | --- |
| `providers` | yes | Non-empty list of `{ provider_id, model_id, weight }`. `model_id` is the upstream model name. |
| `weight` | no | Positive routing weight. Declare weights for all providers or none; declared weights must sum to `1.0`. |
| `max_retry_after` | no | Per-binding cap in seconds on how long a `Retry-After` header may delay the next retry of the same provider. `0` retries immediately; default `5`. |
| `name` | no | Human-readable label shown in the Web UI; the model alias is shown when empty. |
| `context_length` | no | Metadata returned by the models API. |
| `max_output_length` | no | Default output token budget when a request is converted across formats and does not specify one. |
| `modalities` | no | `input` and `output` modality lists returned by the models API. Each defaults to `["text"]`. |
| `timeout` | no | Overrides the provider timeout for this model. |

The map key is the client-visible model alias. It may contain slashes. If no weights are declared, every provider gets weight `1.0` (equal selection probability).

### Users

| Field | Required | Description |
| --- | --- | --- |
| `api_key` | yes | Bearer token or `x-api-key` value. Must be unique. |
| `name` | no | Human-readable label shown in the Web UI; the user ID is shown when empty. |
| `allowed_models` | no | Model allow-list. Omit for unrestricted access; `"*"` is a wildcard. |

The map key is the unique user identifier used in logs.

**Authentication is disabled entirely when there are no users configured.** Do not expose an unauthenticated instance to the internet.

## Web UI

Open `http://127.0.0.1:4000/ui` and sign in with any `api_key` from the `users` section. The console is **read-only**: it has no endpoints for changing providers, models, users, or routing. Configuration changes still require editing `config.yaml` and restarting the process.

The console's API lives under `/api/ui`: `POST /api/ui/login`, `POST /api/ui/logout`, and the session-scoped `GET /api/ui/session`, `/api/ui/overview`, `/api/ui/models`, `/api/ui/providers`, and `/api/ui/requests`.

| Page | Shows |
| --- | --- |
| Overview | Gateway version, uptime, success rate, token totals, 24-hour activity, traffic by model and provider, recent requests |
| Models | Public model aliases with context/output metadata, modalities, provider bindings, upstream model names, and weights |
| Providers | Provider protocols, base URLs, endpoints, retry/timeout settings, proxy scheme, and which of your models use it |
| Requests | Recent model traffic owned by the signed-in key: request ID, model, provider, protocol, stream flag, status, latency, and tokens |
| Account | Your user identity, session expiry, and model allow-list |

The UI honours each user's `allowed_models` ACL: models, providers, and request history are all scoped to the signed-in key. Provider keys, custom headers, and other users' API keys are never sent to the browser.

Details worth knowing:

- **Key login only.** Login exchanges a configured API key for an in-memory session cookie (`HttpOnly`, `SameSite=Strict`, 12-hour TTL). Raw keys are not stored in browser storage. Restarting the gateway invalidates every session.
- **No users, no UI.** When there are no users configured, the Web UI returns `403` with an explanation instead of exposing an unauthenticated console.
- **In-memory statistics.** The dashboard and request log keep the latest 5,000 model requests in process memory. They reset on restart, and they are not a billing or audit system.
- **No writes.** Every UI API route is `GET` except login/logout; other methods return `405`. The only browser state is the session cookie.

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
2. Builds candidates from the model's provider list, dropping providers that cannot serve the request's content (for example, video requests never go to Anthropic endpoints).
3. Orders candidates by weight, unless a sticky entry for the request ID exists.
4. Sends the request to each candidate in order, retrying transient failures up to that provider's `max_retries`.
5. Fails over to the next provider on retryable errors.

Retryable upstream statuses: `408`, `409`, `425`, `429`, `500`, `502`, `503`, `504`, `529` (Anthropic overloaded). Other `4xx` responses are returned to the client immediately. Backoff starts at 250 ms, doubles, is capped at 2 s, and adds up to 50% jitter. `Retry-After` is honored in delta-seconds or HTTP-date form, up to the binding's `max_retry_after` (default 5 s).

After a `429`, that provider is deprioritised for the model that hit the limit: later requests for the same model try other candidates first until the cooldown ends. The cooldown uses the upstream `Retry-After`, or 30 s when the header is absent, and is capped at 5 minutes. Cooldown is a preference, not a hard ban, so the provider is still used when every other candidate is unavailable.

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

Some fields do not have an equivalent in every format and are dropped on a cross-wire hop.

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
- **Reloading:** configuration is loaded once at startup. Restart the process to apply changes. Restarting also clears Web UI sessions and request history.
- **Web UI:** `/ui` is a read-only console; its login, session, and metrics state live in process memory. UI traffic is not included in the request statistics.
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
| `config.go` | YAML loading and validation |
| `ui.go` | Read-only Web UI handlers, key-derived sessions, request history |
| `assets/` | Embedded HTML/CSS/JS console assets (no build step) |
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

- Anthropic endpoints cannot receive video content.
- Cross-wire conversion covers the common chat surface, not every vendor-specific extension.
- No per-user rate limiting, quotas, cost accounting, write/admin UI, or config hot reload. The Web UI is display-only.
- Web UI statistics are per-process and in-memory; they are not persisted and do not survive a restart.
- Sticky routing is per-process and in-memory.
- No built-in TLS termination.

## Security

- Provider keys and user API keys are stored in plaintext in `config.yaml`. Keep the file mode `0600`, mount it read-only in containers, and never commit it.
- With no users configured, anyone who can reach the port can use every model and the Web UI stays disabled. Put the gateway behind a private network, VPN, or authenticating reverse proxy.
- The Web UI exchanges an API key for a session cookie and never exposes provider keys, custom headers, or other users' keys. Serve it over HTTPS through your reverse proxy so the cookie stays confidential.
- Treat configured provider headers as privileged: they are applied after authentication headers and can override them.

## Contributing

Issues and pull requests are welcome. Please run the test suite and `gofmt` before opening a PR, and include tests for protocol-conversion or routing changes.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full text.
