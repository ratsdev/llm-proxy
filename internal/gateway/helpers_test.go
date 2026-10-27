package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/core"
)

// This file holds the fixtures and HTTP helpers shared by the gateway tests.
// Keeping them in one place stops each test file from growing its own near
// duplicate of send/doJSON/decodeBody.

// ------------------------------------------------------------------ fixtures

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

func testRouteSpec(path string) (routeSpec, bool) {
	for _, spec := range routeSpecs {
		if spec.Endpoint == path {
			return spec, true
		}
	}
	return routeSpec{}, false
}

func testConfig(providers []*Provider, model *Model) *Config {
	registry := make(map[string]*Provider, len(providers))
	for _, provider := range providers {
		registry[provider.ID] = provider
	}
	return &Config{
		Providers:  registry,
		Models:     map[string]*Model{model.ID: model},
		UsersByKey: map[string]*User{},
	}
}

const (
	// testAdminUsername is deliberately not "admin": the console must treat the
	// first registered account as the administrator whatever it is called.
	testAdminUsername = "owner"
	testAdminPassword = "test-admin-password"
)

var testAdminPasswordHash = sync.OnceValue(func() string {
	hash, err := core.HashPassword(testAdminPassword)
	if err != nil {
		panic("hash test administrator password: " + err.Error())
	}
	return hash
})

// ------------------------------------------------------------- fake upstream

type fakeRouter struct {
	mu sync.Mutex

	send func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error)

	lastStickyID     string
	lastProtocol     WireFormat
	lastInboundHeads map[string]string
	lastPayload      map[string]any
}

func (f *fakeRouter) Send(_ context.Context, request RouteRequest) (*RouteResult, error) {
	f.mu.Lock()
	f.lastStickyID = request.StickyID
	f.lastInboundHeads = request.InboundHeaders
	f.lastProtocol = request.PreferredFormat
	f.lastPayload = request.Payload
	f.mu.Unlock()

	if f.send == nil {
		return staticResult(request.Model, http.StatusOK, `{"ok":true}`), nil
	}
	return f.send(request.Model, request.Payload, request.StickyID, string(request.PreferredFormat))
}

func staticResult(model *Model, status int, body string) *RouteResult {
	return &RouteResult{
		ProviderID:    "provider",
		UpstreamModel: firstUpstream(model),
		WireFormat:    wireChatCompletions,
		Status:        status,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
	}
}

func errorResult(model *Model, status int, body string) *RouteResult {
	return &RouteResult{
		ProviderID:    "provider",
		UpstreamModel: firstUpstream(model),
		WireFormat:    wireChatCompletions,
		Status:        status,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(body)),
	}
}

func firstUpstream(model *Model) string {
	if model == nil || len(model.Providers) == 0 {
		return "upstream"
	}
	return model.Providers[0].UpstreamModel
}

// ------------------------------------------------------------ http plumbing

func upstream(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func newTestServer(t *testing.T, router routerAPI, mutate func(*Config)) *httptest.Server {
	t.Helper()
	cfg := &Config{
		Providers: map[string]*Provider{
			"provider": testProvider("provider", "http://upstream", "/chat/completions"),
		},
		Models: map[string]*Model{
			"model/a": {ID: "model/a", Modalities: []string{"text"},
				Providers: []ModelProvider{{ProviderID: "provider", UpstreamModel: "model-a"}}},
			"model-b": {ID: "model-b", Modalities: []string{"text"},
				Providers: []ModelProvider{{ProviderID: "provider", UpstreamModel: "model-b"}}},
		},
		UsersByKey: map[string]*User{
			"key-a": {ID: "alice", AllowedModels: []string{"model/a"}},
			"key-b": {ID: "bob", AllowedModels: []string{"*"}},
		},
		UsersByID: map[string]*User{
			testAdminUsername: {
				ID:           testAdminUsername,
				Name:         "Administrator",
				IsAdmin:      true,
				PasswordHash: testAdminPasswordHash(),
			},
		},
		HasAdmin: true,
	}
	if mutate != nil {
		mutate(cfg)
	}
	server := NewServerWithOptions(cfg, router, slog.New(slog.NewTextHandler(io.Discard, nil)), ServerOptions{})
	httptestServer := httptest.NewServer(server.Handler())
	t.Cleanup(httptestServer.Close)
	return httptestServer
}

func doJSON(t *testing.T, method, url, apiKey string, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// readBody returns the raw response body, for assertions about non-JSON replies
// such as event streams.
func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}

func decodeBody(t *testing.T, response *http.Response) map[string]any {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return object
}
