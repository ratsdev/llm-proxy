package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/core"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// Fixtures for the routing-engine tests. They build core configuration values
// directly so the package tests do not depend on the gateway's HTTP helpers.

func testProvider(id, baseURL string, endpoints ...string) *Provider {
	configured := make(map[WireFormat]ProviderEndpoint, len(endpoints))
	for _, path := range endpoints {
		spec, ok := testRouteSpec(path)
		if !ok {
			panic("testProvider: unknown endpoint " + path)
		}
		configured[spec.Wire] = ProviderEndpoint{Path: spec.Endpoint, Converter: convert.None}
	}
	return &Provider{
		ID:         id,
		BaseURL:    baseURL,
		MaxRetries: 2,
		Endpoints:  configured,
		Headers:    map[string]string{},
	}
}

func testRouteSpec(path string) (core.RouteSpec, bool) {
	for _, spec := range routeSpecs {
		if spec.Endpoint == path {
			return spec, true
		}
	}
	return core.RouteSpec{}, false
}

func testConfig(providers []*Provider, model *Model) *Config {
	registry := make(map[string]*Provider, len(providers))
	for _, provider := range providers {
		registry[provider.ID] = provider
	}
	return &Config{
		Providers:  registry,
		Models:     map[string]*Model{model.ID: model},
		UsersByKey: map[string]*core.User{},
	}
}

func upstream(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func textRequest(text string) map[string]any {
	return map[string]any{
		"model":    "public",
		"messages": []any{map[string]any{"role": "user", "content": text}},
	}
}

func noSleep(context.Context, time.Duration) error { return nil }

// send exercises the RouteRequest-based router API.
func send(ctx context.Context, router *Router, model *Model, payload map[string]any,
	headers map[string]string, stickyID string, inbound WireFormat) (*RouteResult, error) {
	stream := false
	if payload != nil {
		stream, _ = util.AsBool(payload["stream"])
	}
	return router.Send(ctx, RouteRequest{
		Model:           model,
		Stream:          stream,
		InboundHeaders:  headers,
		StickyID:        stickyID,
		PreferredFormat: inbound,
		Payload:         payload,
	})
}

func readAll(t *testing.T, request *http.Request) []byte {
	t.Helper()
	if request.Body == nil {
		return nil
	}
	defer request.Body.Close()
	data, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return data
}

// Test-only aliases for the router options that are deliberately unexported in
// production code. They live in a _test file so they never reach the public
// API while in-package tests can still inject deterministic behaviour.
func WithRand(source func() float64) Option { return withRand(source) }

func WithSleep(sleep func(context.Context, time.Duration) error) Option { return withSleep(sleep) }
