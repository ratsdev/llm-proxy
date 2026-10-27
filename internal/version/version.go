// Package version carries the build-time version string.
//
// It is a leaf package with no dependencies so that every layer of the gateway
// (wiring, HTTP surface, routing engine) can report the same value without
// importing one another.
package version

// Value is the gateway version reported by the HTTP API, the console and the
// CLI. It is meant to be replaced at build time:
//
//	go build -ldflags "-X github.com/ratsdev/llm-proxy/internal/version.Value=1.2.3" ./cmd/llm-proxy
var Value = "0.3.0-dev"
