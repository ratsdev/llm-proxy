// Package llmproxy implements a multi-protocol LLM gateway.
//
// The gateway accepts OpenAI Chat Completions, OpenAI Responses and Anthropic
// Messages requests, converts them through a protocol-neutral intermediate
// representation, forwards them to a configured upstream provider and converts
// the response (including server-sent event streams) back into the caller's
// protocol.
//
// This package is the public surface of the module. The implementation lives in
// internal/gateway; only the entry points below and the type aliases they use
// are supported for embedding.
package llmproxy

import (
	"log/slog"

	"github.com/ratsdev/llm-proxy/internal/gateway"
)

// Version is the gateway version reported by the HTTP API and the console. It
// mirrors core.Value, which is the variable patched at build time:
//
//	go build -ldflags "-X github.com/ratsdev/llm-proxy/internal/core.Value=1.2.3" ./cmd/llm-proxy
var Version = gateway.Version

// Re-exported gateway types, so embedders never import the internal package.
type (
	// App is the top-level HTTP handler: it owns the store, the live
	// configuration snapshot and the console, and swaps the routing stack after
	// an administrative change.
	App = gateway.App
	// Config is one immutable configuration snapshot.
	Config = gateway.Config
	// Store owns the SQLite database that holds every configurable record.
	Store = gateway.Store
	// User is a console account with its model allow-list.
	User = gateway.User
	// Model is a caller-visible model and its upstream provider bindings.
	Model = gateway.Model
	// Provider is one upstream service shared by its configured endpoints.
	Provider = gateway.Provider
)

const (
	// DefaultDatabasePath is used when --db is not supplied.
	DefaultDatabasePath = gateway.DefaultDatabasePath
)

// NewApp loads the current configuration from the store and builds the first
// routing stack.
func NewApp(store *Store, logger *slog.Logger) (*App, error) {
	return gateway.NewApp(store, logger)
}

// OpenStore opens (and creates when necessary) the SQLite database.
func OpenStore(path string) (*Store, error) {
	return gateway.OpenStore(path)
}
