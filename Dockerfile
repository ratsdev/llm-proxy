# syntax=docker/dockerfile:1

# ---- build stage -----------------------------------------------------------
# The build image is only used to compile a static binary; nothing from it is
# carried into the final image.
FROM golang:1.26.1-alpine AS build

WORKDIR /src

# Download dependencies in their own layer so source edits do not invalidate
# the module cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev

# CGO is disabled so the result runs on plain Alpine without libc shims.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build \
        -trimpath \
        -ldflags="-s -w -X github.com/ratsdev/llm-proxy/internal/core.Value=${VERSION}" \
        -o /out/llm-proxy ./cmd/llm-proxy

# ---- runtime stage ---------------------------------------------------------
FROM alpine:3

# alpine:3 already ships the Mozilla CA bundle at
# /etc/ssl/certs/ca-certificates.crt, so no apk fetch is needed for TLS.
COPY --from=build /out/llm-proxy /usr/local/bin/llm-proxy

# The process holds provider credentials and gateway API keys, so it runs as an
# unprivileged user. /data is owned by that user before the volume is declared,
# which keeps a named volume writable without a chown on the host.
RUN addgroup -S -g 10001 llmproxy \
 && adduser -S -u 10001 -G llmproxy -H -s /sbin/nologin llmproxy \
 && mkdir -p /data \
 && chown llmproxy:llmproxy /data

VOLUME ["/data"]

USER 10001:10001

EXPOSE 4000

# /api/health is unauthenticated; this assumes the default port 4000.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:4000/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/llm-proxy"]
CMD ["--db", "/data/llm-proxy.db"]
