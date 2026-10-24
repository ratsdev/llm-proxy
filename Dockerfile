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
        -ldflags="-s -w -X github.com/ratsdev/llm-proxy.Version=${VERSION}" \
        -o /out/llm-proxy ./cmd/llm-proxy

# ---- runtime stage ---------------------------------------------------------
FROM alpine:3

# alpine:3 already ships the Mozilla CA bundle at
# /etc/ssl/certs/ca-certificates.crt, so no apk fetch is needed for TLS.
COPY --from=build /out/llm-proxy /usr/local/bin/llm-proxy

# Configuration is mounted at runtime, never baked into the image. The process
# runs as root so it can read a config mounted at mode 0600 and owned by root.
RUN mkdir -p /etc/llm-proxy

EXPOSE 4000

# /api/health is unauthenticated; this assumes the default port 4000.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:4000/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/llm-proxy"]
CMD ["--config", "/etc/llm-proxy/config.toml"]
