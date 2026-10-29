package core

// Value is the gateway version reported by the HTTP API, the console and the
// CLI. It is meant to be replaced at build time:
//
//	go build -ldflags "-X github.com/ratsdev/llm-proxy/internal/core.Value=1.2.3" ./cmd/llm-proxy
var Value = "0.3.0-dev"
