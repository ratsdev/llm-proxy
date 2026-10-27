# llm-proxy

[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

A self-hosted, multi-endpoint LLM gateway written in Go. Give every client one base URL, route requests to many upstream providers, and manage the whole gateway from a built-in Web console backed by SQLite.

```
                    ┌──────────────────────────────────────────┐
  OpenAI SDK ──────▶│ /api/v1/chat/completions                 │
  OpenAI Responses ─▶│ /api/v1/responses                        │──▶ Router ──▶ OpenAI-compatible APIs
  Anthropic SDK ───▶│ /api/v1/messages                         │            │        Anthropic-compatible APIs
                    └──────────────────────────────────────────┘            │
                                                              priority, round-robin or random routing, retries, failover
```

## Why

- **One key, many providers.** Clients use a single model alias; the gateway can fan out to one or more upstreams with priority, round-robin, or random selection.
- **No configuration files.** Providers, models, users, and routing are stored in SQLite and edited in the Web console. Changes are hot-reloaded without a restart.
- **Per-endpoint protocol conversion.** Each provider endpoint names the converter that transforms its traffic. Choose `none` to forward the client's protocol unchanged, or a registered cross-protocol converter to translate requests, responses, and SSE events.
- **Self-hosted and small.** One static binary, one SQLite file, no external control plane.

## Features

- **Three independent endpoints:** OpenAI Chat, OpenAI Responses, and Claude Messages. A provider has one shared base URL; each endpoint adds its own upstream path and converter.
- **Explicit endpoint converters.** An endpoint sets `converter` to `none` or to a directional converter ID such as `openai_chat_to_claude_messages`. `none` forwards the inbound wire format unchanged.
- **Three-protocol conversion.** All six directions among `chat_completions`, `responses`, and `messages` are supported for requests, non-streaming responses, and streaming responses. The converters share the protocol-neutral IR in `internal/convert/ir/`; Gemini is intentionally out of scope.
- **Lossless passthrough for same-protocol traffic.** JSON and SSE bodies are forwarded as-is in both streaming and non-streaming modes, with only the public model name rewritten, so `logprobs`, `system_fingerprint`, `service_tier`, extra choices, and vendor extensions survive. Cross-protocol traffic goes through the IR. Token usage is still extracted from both JSON replies and SSE events for the dashboard.
- **Routing strategies, retries, and failover.** Priority, round-robin, or random selection of the first provider; per-provider retry budgets, exponential backoff, `Retry-After` support, and automatic failover to the next candidate.
- **Conversation affinity.** A session stays on the upstream that served it. Send `x-session-id` (or `x-conversation-id`) for a hard guarantee; otherwise the gateway fingerprints the first user message or uses `prompt_cache_key`.
- **Streaming.** SSE bytes are relayed incrementally. An interrupted stream ends with the endpoint's terminal error event instead of a silent truncation.
- **Web-configured SQLite store.** Provider catalogs, model alias bindings and capabilities, console accounts, and per-user API keys, plus password changes. Every write reloads the live routing stack atomically.
- **Administrator registration.** No account is created on start. Open the Web console, register the first account, and that account becomes the administrator; any username is allowed. Registration closes as soon as the administrator exists.
- **Optional self-registration.** An administrator can enable member self-registration and choose the default model allow-list assigned to new accounts. The switch and defaults are managed from **Administration → Settings**.
- **Accounts and self-service keys.** Administrators create ordinary users, who sign in to the console with a username and password and manage their own gateway API keys. Optional per-user model allow-lists. API authentication is disabled while no API keys exist.
- **Model listing.** OpenAI-compatible `GET /api/v1/models` and `GET /api/v1/models/{id}`, filtered by the caller's ACL.
- **Web console.** Dashboard, models, providers, users, API keys, recent requests, and account settings at `/`.
- **Operational niceties.** Health endpoint, one access-log line per API operation, request IDs, provider response headers, graceful shutdown, and a small multi-stage Docker image.

## Supported endpoints

| Inbound endpoint | Endpoint type | Default upstream path |
| --- | --- | --- |
| `POST /api/v1/chat/completions` | `chat_completions` | `/chat/completions` |
| `POST /api/v1/responses` | `responses` | `/responses` |
| `POST /api/v1/messages` | `messages` | `/v1/messages` |

A provider endpoint's `converter` must be `none` or a registered converter whose source protocol matches the inbound endpoint type. An OpenAI Chat request is served only by a provider that configures the `chat_completions` endpoint; it is never silently routed to a Claude Messages-only endpoint.

The gateway also exposes:

| Endpoint | Description |
| --- | --- |
| `GET /api` | Gateway name, version, model count, advertised routes, and registered converters |
| `GET /api/health` | Unauthenticated health probe |
| `GET /api/v1/models` | OpenAI-style model list (ACL-filtered) |
| `GET /api/v1/models/{model_id}` | Model metadata (ACL-checked) |
| `GET /` | Web console |

### Provider endpoints

A provider has one base URL shared by every endpoint. Each configured endpoint adds its own upstream path and converter:

| Inbound endpoint type | Default path | Selectable converters |
| --- | --- | --- |
| `chat_completions` | `/chat/completions` | `none`, `openai_chat_to_claude_messages`, `openai_chat_to_openai_responses` |
| `responses` | `/responses` | `none`, `openai_responses_to_openai_chat`, `openai_responses_to_claude_messages` |
| `messages` | `/v1/messages` | `none`, `claude_messages_to_openai_chat`, `claude_messages_to_openai_responses` |

`converter: none` forwards the client's wire format unchanged. A cross-protocol converter changes both the request sent upstream and the reply returned to the client; its source protocol must match the inbound endpoint type. An unknown converter, or one for a different source, is rejected when the provider is saved. A provider must configure at least one endpoint; endpoint types left out are not served by that provider.

## Quick start

### From source

Requires Go 1.26 or newer.

```bash
git clone https://github.com/ratsdev/llm-proxy.git
cd llm-proxy

go build -o bin/llm-proxy ./cmd/llm-proxy
./bin/llm-proxy --db ./llm-proxy.db
```

The first start creates no accounts. Open `http://127.0.0.1:4000/` and the
console asks you to register the administrator: choose any username and a
password of at least eight characters. That first account receives administrator
rights and is signed in immediately. Registration closes once the administrator
exists, so later visitors sign in normally and cannot claim the gateway.

There is no bootstrap password file and no initial-password flag. If the
administrator password is lost, remove the database file and start over.

Every API route lives under `/api`, so the base URL is `http://127.0.0.1:4000/api/`; append `/v1` for OpenAI-style clients (for example `http://127.0.0.1:4000/api/v1`).

### Docker

```bash
docker run --rm -p 4000:4000 \
  -v llm-proxy-data:/data \
  ghcr.io/ratsdev/llm-proxy:latest
```

The image stores its database at `/data/llm-proxy.db` and declares `/data` as a volume. It runs as an unprivileged user (uid 10001) that owns `/data`. Open the console and register the first administrator there; no password file is generated.

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

### CLI flags

```
llm-proxy [--db <path>] [--host <addr>] [--port <port>] [--log-level <level>] [--version]

      --db                path to the SQLite database, created on first start (default: llm-proxy.db)
      --host              listen address (default: 0.0.0.0)
      --port              listen port (default: 4000)
      --log-level         debug | info | warning | error (default: info)
      --version           print the version and exit
```

Environment variables are used when a flag is omitted: `LLM_PROXY_DB`, `LLM_PROXY_HOST`, `LLM_PROXY_PORT`, and `LLM_PROXY_LOG_LEVEL`.

## Configuration

There is no YAML configuration and no configuration file to mount. Everything is stored in SQLite and managed from `/`:

- **Providers** — one shared base URL, per-endpoint upstream paths and converters, API keys, retry/timeout settings, custom headers, proxy, and the catalog of upstream model IDs.
- **Models** — public alias and capability metadata plus bindings to upstream models from a provider's catalog, with a per-model routing strategy.
- **Users** — console accounts, optional model allow-lists, and each account's own API keys.
- **Account** — password and session details.

Changes are validated, written to the database, and activated immediately. The gateway builds a new routing stack and atomically swaps it in, so in-flight requests finish on the stack they started with.

### Providers

Each provider has one base URL, one or more endpoint types, and an optional catalog of upstream model IDs. In the Web UI, endpoints are edited as NewAPI-style rows: choose an endpoint type, choose its converter, and optionally override the upstream path. The API shape is:

| Field | Required | Description |
| --- | --- | --- |
| `id` | yes | Provider identifier. Must be non-empty, without leading/trailing whitespace or control characters. It cannot be changed after creation. |
| `base_url` | yes | Shared upstream base URL. Must be an `http` or `https` URL; trailing slashes are stripped. |
| `endpoints` | yes | Mapping keyed by endpoint type (`chat_completions`, `responses`, `messages`). At least one endpoint is required. Each value is `{ "path": "/...", "converter": "none" }`. `path` is optional, defaults to the endpoint's default path, and is joined to `base_url`'s path; a query string on the base URL is preserved. `converter` defaults to `none`; any other value must be a registered converter whose source matches the endpoint type. |
| `api_key` | no | Sent as `Authorization: Bearer <key>` for OpenAI Chat and OpenAI Responses, and as `x-api-key: <key>` for Claude Messages. On an update, a blank value keeps the stored key. |
| `name` | no | Human-readable label shown in the Web UI; the provider ID is shown when empty. |
| `max_retries` | no | Retries per request on transient failures, `0`-`10`, default `2`. |
| `timeout` | no | Request and stream-idle timeout in seconds. Default `600`. |
| `headers` | no | Extra headers applied last. They can override `Authorization`, `Accept`, and other defaults. |
| `proxy` | no | HTTP(S) or SOCKS5 proxy for this provider. Environment proxy variables are never consulted. |
| `upstream_models` | no | Upstream model IDs this provider serves. The Models page binds public aliases to these IDs. When the list is non-empty, a model binding must use one of its entries. |

### Models

Models only bind a public alias to an upstream model and describe its capabilities. Upstream model IDs are configured on the provider.

| Field | Required | Description |
| --- | --- | --- |
| `id` | yes | Client-visible model alias. May contain slashes. It cannot be changed after creation. |
| `providers` | yes | Non-empty list of `{ provider_id, upstream_model, max_retry_after? }`. `upstream_model` is the provider's own model name and must appear in the provider's `upstream_models` catalog when that catalog is not empty. Bindings are tried in list order for priority routing. |
| `routing` | no | `priority` (default), `round_robin`, or `random`. Selects the first upstream for a new conversation. |
| `max_retry_after` | no | Per-binding cap in seconds on how long a `Retry-After` header may delay the next retry of the same provider. `0` retries immediately; default `5`. |
| `name` | no | Human-readable label shown in the Web UI; the alias is shown when empty. |
| `context_length` | no | Metadata returned by the models API. |
| `max_output_length` | no | Metadata returned by the models API. |
| `modalities` | no | Input modality list returned by the models API. Defaults to `["text"]`. |
| `output_modalities` | no | Output modality list returned by the models API. Defaults to `["text"]`. |
| `reasoning` | no | Whether the model accepts reasoning controls. Returned as `reasoning` by the models API. Defaults to `false`. |
| `tools` | no | Whether the model accepts tool/function calling. Returned as `tools` by the models API. Defaults to `false`. |
| `timeout` | no | Overrides the provider timeout for this model. |

The routing strategy only picks the first provider for a new conversation. Once a provider has served a request, the conversation stays pinned to it. Without an explicit session header, affinity is derived from `prompt_cache_key` or the first user message; see [Conversation affinity](#conversation-affinity).

### Users and API keys

A console account signs in with a username and password. Administrators can create ordinary users; members can open **Models**, **API keys**, and **Account**. Each account owns its API keys, and an administrator cannot view or delete another account's keys. The member **Models** page is a read-only list of the models the signed-in account may call; it never exposes provider bindings or upstream credentials.

| Field | Required | Description |
| --- | --- | --- |
| `id` | yes | Unique account identifier and login username. Must be non-empty, without leading/trailing whitespace or control characters. It cannot be changed after creation. |
| `name` | no | Human-readable label shown in the Web UI; the user ID is shown when empty. |
| `password` | on create | At least 8 characters. Leave blank on update to keep the current password. |
| `allowed_models` | no | Model allow-list. An empty list grants no models; `"*"` allows every model, including models added later. The console edits this list with checkboxes, and **Select all** stores `"*"`. |
| `is_admin` | no | Set only by first-run registration. Accounts created from the console are ordinary members. |

Each account can create up to 25 API keys from the **API keys** page. A key is sent as `Authorization: Bearer <key>` or `x-api-key: <key>` and inherits the owning account's model allow-list.

| Field | Required | Description |
| --- | --- | --- |
| `name` | no | Label used to recognize the key. Defaults to `API key`. |

**API authentication is disabled while no API keys exist.** Create at least one key before exposing the gateway to clients. Do not expose an unauthenticated instance to the internet.

### Registration settings

Administrators manage the registration policy from **Administration → Settings** (`#/admin/settings`):

| Setting | Default | Description |
| --- | --- | --- |
| `self_registration` | `false` | When enabled, visitors can create their own member account from the sign-in screen. First-run administrator registration is always available regardless of this switch. |
| `default_allowed_models` | `[]` | Model allow-list assigned to every self-registered account. `[]` grants no models; `["*"]` grants all models, including models added later. |

The policy is stored in the `settings` table, hot-reloaded after saving, and exposed through `GET`/`PUT /api/ui/settings` to administrators and the public `GET /api/ui/registration` status used by the sign-in screen. The public status returns only whether first-run registration is required and whether self-registration is open; it never exposes the default allow-list. Registration attempts are throttled per source address (twenty per hour) so one visitor cannot create accounts, probe usernames, or spend password-hashing work without limit; a duplicate username gets the same generic conflict response as other rejected registrations.

### Administrator account

The administrator is the first account registered from the Web console, using
whatever username the operator chooses. No administrator is created on startup;
until that account exists, the console offers registration instead of sign-in.
Once it exists, administrator registration is closed; member self-registration
follows the **Settings** policy. Register the administrator immediately after the
first start: until then, anyone who can reach the console can claim it.
Administrators can allow member self-registration and set the default model
allow-list for those accounts from **Administration → Settings**.
Passwords are stored as PBKDF2-HMAC-SHA256 verifiers
(`pbkdf2-sha256$210000$salt$hash`); plaintext passwords are never stored or
returned by the API. Sign-in attempts are throttled per account and per source
address after five failures in five minutes.

Change the password from the Account page. Doing so keeps the current session
and invalidates every other session for that account. The administrator cannot
be deleted or edited through the user endpoints.

## Web UI

Open `http://127.0.0.1:4000/`.

- **Sign-in:** every account uses its username and password. A fresh deployment shows registration instead; the first account created becomes the administrator. When self-registration is enabled, the sign-in screen also offers **Create an Account**. The console does not accept an API key as a login credential; keys are only for model requests.
- **Administrators** see Overview, Models, Requests, API keys, and Account in the sidebar. Model, provider, user, and registration management moved out of the sidebar and open from the header **Manage** button at `#/admin/models`, `#/admin/providers`, `#/admin/users`, and `#/admin/settings`; these pages share a stable **Administration** heading and a tab bar.
- **Members** can open Overview, Models, Requests, API keys, and Account. Their Overview and Requests views are scoped to their own traffic, and provider names, upstream model IDs, provider counts and provider breakdowns are hidden from them.
- **Request history** is shared in memory, but administrators see every account while members see only their own requests.
- **API keys** shows every gateway endpoint URL with copy buttons, alongside each account's own keys.

| Page | Shows |
| --- | --- |
| Overview | Gateway version, uptime, success rate, token totals, 24-hour activity, model traffic and recent requests; administrators also see provider traffic. Members see only their own requests |
| Models | Card grid of every model the signed-in account may call, filtered by its `allowed_models` list |
| Models (administration) | Public aliases, upstream bindings, input/output modalities, context/output limits, reasoning and tool support; create, edit, and delete |
| Providers | NewAPI-style endpoint rows with type, converter, and path, upstream model catalog, retry/timeout settings, and proxy scheme; create, edit, and delete |
| Users | Console accounts, roles, API-key counts, and allow-lists; administrators create, edit, and delete ordinary users |
| Settings (administration) | Self-registration switch and the default model allow-list assigned to self-registered accounts |
| API keys | Full gateway endpoint URLs plus every account's own credentials: create, copy, and delete |
| Account | Identity, role, session expiry, and password change |
| Requests | Recent model traffic: request ID, model, endpoint type, stream flag, status, latency, and tokens; provider details are administrator-only, and members see only their own requests |

The console's API lives under `/api/ui`: `POST /api/ui/register` (first administrator or self-registration), the public `GET /api/ui/registration` status, `POST /api/ui/login`, `POST /api/ui/logout`, `POST /api/ui/password`, `GET`/`PUT /api/ui/settings` for administrators, the member-facing `GET /api/ui/catalog`, authenticated reads, CRUD under `/api/ui/providers`, `/api/ui/models`, and `/api/ui/users`, and self-service key management under `/api/ui/keys`.

Details worth knowing:

- **Session cookie.** Login exchanges credentials for an in-memory session cookie (`HttpOnly`, `SameSite=Strict`, 12-hour TTL). Raw keys and passwords are not stored in browser storage. Restarting the gateway invalidates every session.
- **Secrets.** Provider keys and custom headers are redacted from list responses; the provider edit form returns the stored credentials to an administrator session. API keys are returned only to their owner. Raw passwords are never returned.
- **In-memory statistics.** The dashboard and request log keep the latest 5,000 model requests in process memory. They reset on restart and are not a billing or audit system.
- **Immediate activation.** Successful writes are hot-reloaded; there is no restart step.

## Usage

### OpenAI Chat

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
    "model": "responses-default",
    "input": "Explain SSE in one sentence."
  }'
```

### Claude Messages

```bash
curl http://127.0.0.1:4000/api/v1/messages \
  -H "x-api-key: sk-alice" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-default",
    "max_tokens": 512,
    "messages": [{"role": "user", "content": "Explain SSE in one sentence."}]
  }'
```

Each model must be bound to a provider that configures the matching endpoint type.

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

An Anthropic SDK can point at the same gateway when the model is bound to a provider with the `messages` endpoint:

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://127.0.0.1:4000/api",
    api_key="sk-alice",
)

message = client.messages.create(
    model="claude-default",
    max_tokens=256,
    messages=[{"role": "user", "content": "Hello"}],
)
print(message.content[0].text)
```

## Routing and reliability

For each request the gateway:

1. Authenticates the caller and resolves the model alias.
2. Builds candidates from the model's provider bindings, keeping only providers that configure the inbound endpoint type and whose converter is valid for that inbound protocol.
3. Orders candidates with the model's routing strategy, unless a sticky entry for the session exists.
4. Sends the request to each candidate in order, retrying transient failures up to that provider's `max_retries`.
5. Fails over to the next provider on retryable errors.

Routing strategies:

| Strategy | Behaviour |
| --- | --- |
| `priority` | Tries providers in the order they are declared. Reorder bindings in the model editor to change the order. |
| `round_robin` | Rotates the starting provider for each new conversation. |
| `random` | Shuffles the candidate order for each new conversation. |

The strategy affects only a conversation's first successful request. After that, the session is pinned to the provider that served it, and failover re-pins the session to the provider that eventually succeeds.

If no candidate can serve the inbound wire format, the request fails before any upstream is contacted.

Retryable upstream statuses: `408`, `409`, `425`, `429`, `500`, `502`, `503`, `504`, `529` (Anthropic overloaded). Other `4xx` responses are returned to the client immediately. Backoff starts at 250 ms, doubles, is capped at 2 s, and adds up to 50% jitter. `Retry-After` is honored in delta-seconds or HTTP-date form, up to the binding's `max_retry_after` (default 5 s).

Retries are not free: a completion request is not idempotent, and a transport
failure can happen after the upstream has already accepted and billed the
request. The gateway retries because the alternative (surfacing a transient
network error) is usually worse for clients, but treat the retry budget as a
cost multiplier when a provider charges per attempt.

After a `429`, that provider is deprioritised for the model that hit the limit: later requests for the same model try other candidates first until the cooldown ends. The cooldown uses the upstream `Retry-After`, or 30 s when the header is absent, and is capped at 5 minutes. Cooldown is a preference, not a hard ban, so the provider is still used when every other candidate is unavailable.

### Conversation affinity

A successful upstream is remembered for the session and preferred by later requests in the same conversation. The pin cache is in-memory, bounded to 10,000 entries, and is not shared between replicas.

The session key is chosen in this order:

1. `x-session-id` or `x-conversation-id` header. This is the explicit guarantee: send the same value on every turn and the conversation cannot move unless the pinned provider fails.
2. `prompt_cache_key` from the request body, which clients already use as an affinity hint.
3. A fingerprint of the first user message (`messages[]` or Responses `input`).

The authenticated user and model alias are mixed into the key, so a pin never leaks across tenants or model names. If none of the sources above is present, the gateway falls back to a valid `x-request-id`, scoped the same way; requests without a valid request ID are not pinned.

Content fingerprinting assumes the client resends a stable first user message on every turn. Conversations that open with identical text can share a provider; use an explicit session header when that matters.

**Response headers:**

| Header | Meaning |
| --- | --- |
| `x-request-id` | Client-supplied or generated request ID. |
| `x-provider-id` | Upstream provider that served the request. |
| `x-upstream-model` | Upstream model name that was called. |
| `Retry-After` | Forwarded from the upstream on errors. |

## Endpoint handling

Every wire format parses into the protocol-neutral IR in `internal/convert/ir/`, and every target format is rendered from it. Same-protocol traffic skips the IR so unknown fields survive.

- **Request:** with `converter: none` the inbound JSON body is forwarded in the client's wire format, with `model` replaced by the upstream model name. With a cross-protocol converter, the body is parsed into IR and rendered in the upstream protocol.
- **Non-streaming response:** the upstream JSON is relayed as JSON regardless of whether the client asked for a stream. Same-protocol bytes are forwarded as-is apart from the public model name, so field order and unknown fields are preserved. Cross-protocol replies are rendered in the client's protocol, and usage is parsed in both modes for the dashboard.
- **Streaming response:** SSE is relayed incrementally whenever the upstream returns SSE, even to a caller that did not set `stream`. Same-protocol frames have their `model` fields rewritten in place; cross-protocol frames are decoded from the upstream dialect and re-encoded in the client's dialect.
- **Modes pass through:** the client's `stream` flag is forwarded upstream and never used to rewrite the reply shape. A JSON reply stays JSON and an SSE reply stays SSE.
- **Usage:** token counts are extracted from non-streaming JSON and streaming SSE replies in both modes. TTFT/ITL are not computed for non-streaming replies; only input, output, and total token counts are recorded.
- **Interrupted stream:** the client receives the endpoint's terminal error event (`[DONE]` for OpenAI Chat, `event: error` for Claude Messages, `event: response.failed` for OpenAI Responses).
- **Upstream ignored `stream`:** a complete JSON reply is relayed as JSON rather than synthesised into a stream.

`GET /api` and `GET /api/ui/converters` list the selectable converters. A converter is rejected when the provider is saved if its source protocol does not match the endpoint. A non-streaming conversion failure returns `502 upstream_error`; a broken cross-protocol stream ends with the target protocol's error event.

## Operations

- **Health:** `GET /api/health` is unauthenticated and returns model/provider counts.
- **Index:** `GET /api` returns the version, advertised endpoints, and registered converters.
- **Errors:** endpoint-native JSON envelopes. Request failures include a stable `code` such as `model_not_found`, `model_not_allowed`, `invalid_json`, `unsupported_conversion`, or `upstream_error`.
- **Request size:** request bodies are limited to 64 MiB.
- **Response size:** buffered upstream replies are limited to 64 MiB; a single SSE line is limited to 8 MiB.
- **Logging:** one structured access-log line per API operation, including request ID, user, model, provider, stream flag, status, duration, and token usage when known. Health and index probes are not logged.
- **Timeouts:** model timeout overrides provider timeout, which defaults to 600 seconds. The timeout applies to the request and to stream inactivity. A single upstream stream is additionally capped at one hour, so a provider that keeps trickling bytes cannot hold a request open forever.
- **Shutdown:** `SIGINT`/`SIGTERM` triggers a graceful shutdown with a 30-second deadline.
- **Reloading:** configuration changes from the Web UI are applied immediately. The conversation pins, 429 cooldowns and round-robin cursors survive a reload, and upstream connections are reused; restarting the gateway clears them along with Web UI sessions and request history.
- **Database:** SQLite runs with foreign keys, WAL journaling, and a 5-second busy timeout. Back up the single database file (and its `-wal`/`-shm` sidecars while running).
- **TLS:** the gateway speaks plain HTTP. Terminate TLS in a reverse proxy or ingress.

## Architecture

The module root is the public surface; everything else lives under `internal/`.

```
llmproxy.go                 package llmproxy: Version, NewApp, OpenStore + type aliases
cmd/llm-proxy/              CLI entry point, flags, startup, shutdown
internal/
  version/                  build-time version (ldflags target)
  core/                     configuration, routes, SQLite store, password hashing
  convert/                  converter registry + protocol-neutral IR + adapters
    ir/                     request, response, stream event and usage types
    protocols/              OpenAI Chat, OpenAI Responses, Claude Messages adapters
  router/                   upstream selection, retries, sticky cache, HTTP clients
  gateway/                  HTTP surface: routes, auth, relay, console wiring, App
  ui/                       embedded console (no build step) and its JSON API
  util/                     error envelope, JSON helpers, SSE parser, HTTP bodies, IDs
```

| Path | Purpose |
| --- | --- |
| `llmproxy.go` | Public API: `Version`, `NewApp`, `OpenStore`, `DefaultDatabasePath`, type aliases |
| `cmd/llm-proxy/` | CLI entry point, flags, startup, graceful shutdown |
| `internal/version/` | Version string; the only ldflags target |
| `internal/core/config.go` | Runtime configuration types, endpoint validation, entity builders |
| `internal/core/wire.go` | Single source of truth for routes and endpoints; aliases `convert.Format` |
| `internal/core/store.go` | Store lifecycle, password changes, input types, codecs |
| `internal/core/store_load.go` | Configuration snapshot loading (providers, models, users, keys) |
| `internal/core/store_catalog.go` | Provider and model CRUD with catalog and binding validation |
| `internal/core/store_accounts.go` | First-administrator registration, self-service member registration, user and API-key CRUD |
| `internal/core/store_settings.go` | Registration policy settings and defaults in the `settings` table |
| `internal/core/store_migrate.go` | Schema creation plus migrations for older databases |
| `internal/core/password.go` | PBKDF2 hashing and verification |
| `internal/convert/` | Converter registry (`AdapterFor`), request/response entry points, error envelopes |
| `internal/convert/ir/` | Protocol-neutral types, including the shared usage parser |
| `internal/convert/protocols/` | Wire-format adapters (`openai/`, `anthropic/`) |
| `internal/router/router.go` | Candidate selection, routing strategies, dispatch, failover |
| `internal/router/router_retry.go` | Retry loop, upstream execution, Retry-After and backoff |
| `internal/router/router_sticky.go` | Sticky cache and 429 cooldowns |
| `internal/router/session.go` | Conversation-affinity key derivation |
| `internal/router/router_headers.go` | Upstream headers and per-model/per-provider timeouts |
| `internal/router/client.go` | Upstream HTTP clients, timeouts, HTTP/SOCKS5 proxies |
| `internal/gateway/app.go` | `App`: owns the store, console handler and atomically swapped routing stack |
| `internal/gateway/server.go` | Server construction, route table, console wiring |
| `internal/gateway/middleware.go` | Request id, panic recovery, access log, response recorder |
| `internal/gateway/auth.go` | API-key authentication and model allow-list enforcement |
| `internal/gateway/api_models.go` | Index, health and model listing/retrieval endpoints |
| `internal/gateway/httpjson.go` | Error envelope rendering, body decoding, inbound protocol headers |
| `internal/gateway/proxy.go` | Inbound request handling; selects and invokes the relay path |
| `internal/gateway/relay.go` | JSON and SSE relay, stream framing, model-name rewriting |
| `internal/gateway/relay_usage.go` | Token accounting for passthrough responses and streams |
| `internal/ui/` | Console handler, sessions, login throttling, dashboard, admin CRUD, embedded assets |
| `internal/util/` | Client-facing error type, JSON helpers and field rewriting, SSE parser, body/response helpers, `crypto/rand` identifiers |

New wire-format work belongs in `internal/convert/protocols/` and `internal/convert/ir/`; `internal/convert/` exposes the selectable registry and dispatch. Add a converter ID only when the direction is implemented and tested.

Test files sit next to the file they cover (`store_catalog_test.go` beside `store_catalog.go`), and shared fixtures live in `helpers_test.go` per package.

## Development

Every push and pull request runs `.github/workflows/ci.yml`: formatting, `go vet`,
`golangci-lint` with `.golangci.yml`, and race-enabled tests with coverage. The
same checks locally:

```bash
gofmt -l .                 # must print nothing
go vet ./...
golangci-lint run ./...    # v2.10.1, configured by .golangci.yml
go test -race -cover ./...
```

Build with a version string:

```bash
go build -trimpath \
  -ldflags "-s -w -X github.com/ratsdev/llm-proxy/internal/version.Value=1.0.0" \
  -o bin/llm-proxy ./cmd/llm-proxy
```

## Limitations

- Conversion covers `chat_completions`, `responses`, and `messages`; Gemini and other protocols are not supported.
- No TTFT/ITL metrics; non-streaming requests record token usage only.
- No per-user rate limiting, quotas, or cost accounting.
- The gateway assumes a single instance: routing state, console sessions, request history and 429 cooldowns live in one process, and SQLite allows one writer. Run one replica per database; scale by giving different clients different instances, not by sharing one.
- Console sign-in is throttled per account and per source address: five rejected attempts in five minutes delay that account for the rest of the window. The account-wide bucket resists distributed guessing but also lets anyone delay a known account for a few minutes; there is no captcha and no multi-factor login.
- Web UI statistics are per-process and in-memory; they are not persisted and do not survive a restart.
- Session pins are per-process and in-memory.
- No built-in TLS termination.
- Console accounts have two roles, administrator and member; there is no finer-grained permission system.

## Security

- Provider keys and gateway API keys are stored in plaintext in the SQLite database, because the gateway must replay them upstream and the console must show a key to its owner once. New database files are created with owner-only permissions (`0600`). Protect existing database files, backups, and the `/data` volume.
- Gateway credentials are 128-bit `crypto/rand` values. Identifier generation fails closed: if the system entropy source fails, key and session creation return an error instead of a predictable value.
- Console passwords are stored as salted PBKDF2-HMAC-SHA256 verifiers, not plaintext.
- With no API keys configured, anyone who can reach the port can use every model. The Web UI still requires a console login. Put the gateway behind a private network, VPN, or authenticating reverse proxy.
- The Users page and the provider edit form expose stored provider secrets to an administrator session. API keys are visible only to their owner. Serve the console over HTTPS through your reverse proxy so the session cookie stays confidential.
- Treat configured provider headers as privileged: they are applied after authentication headers and can override them.
- The session cookie's `Secure` flag is derived from the connection, or from `X-Forwarded-Proto` when TLS terminates in front of the gateway. Only run behind a proxy you control, and make sure it sets that header.
- `GET /api/health` is unauthenticated. Expose the port only on a network you trust.

## Contributing

Issues and pull requests are welcome. Please run the test suite and `gofmt` before opening a PR, and include tests for converter, routing, or store changes.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full text.
