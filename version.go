// Package llmproxy implements a multi-protocol LLM gateway.
//
// The gateway accepts OpenAI Chat Completions, OpenAI Responses and Anthropic
// Messages requests, converts them through a protocol-neutral intermediate
// representation, forwards them to a configured upstream provider and converts
// the response (including server-sent event streams) back into the caller's
// protocol.
package llmproxy

// Version is the gateway version reported by the HTTP API. It may be
// overridden at build time with:
//
//	go build -ldflags "-X github.com/ratsdev/llm-proxy.Version=1.2.3" ./cmd/llm-proxy
var Version = "0.2.0-dev"
