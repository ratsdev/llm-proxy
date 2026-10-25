package llmproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

func testProvider(id, baseURL string, protocol ProviderProtocol, endpoints ...string) *Provider {
	configured := make([]ProviderEndpoint, 0, len(endpoints))
	for _, path := range endpoints {
		spec, ok := testRouteSpec(path)
		if !ok {
			panic("testProvider: unknown endpoint " + path)
		}
		configured = append(configured, ProviderEndpoint{
			Name: spec.configKey,
			Path: spec.endpoint,
			Wire: spec.wire,
		})
	}
	return &Provider{
		ID:         id,
		BaseURLs:   map[ProviderProtocol]string{protocol: baseURL},
		MaxRetries: 2,
		Endpoints:  map[ProviderProtocol][]ProviderEndpoint{protocol: configured},
		Headers:    map[string]string{},
	}
}

func testRouteSpec(path string) (routeSpec, bool) {
	for _, spec := range routeSpecs {
		if spec.endpoint == path {
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

func upstream(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func textRequest(text string) *ir.Request {
	return &ir.Request{Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Block{ir.TextBlock(text)}}}}
}

func noSleep(context.Context, time.Duration) error { return nil }

// send is a test helper for the RouteRequest-based router API.
func send(router *Router, ctx context.Context, model *Model, request *ir.Request,
	headers map[string]string, stickyID string, inbound WireFormat) (*RouteResult, error) {
	return router.Send(ctx, RouteRequest{
		Model:           model,
		Stream:          request != nil && request.Stream,
		InboundHeaders:  headers,
		StickyID:        stickyID,
		PreferredFormat: inbound,
		Request:         request,
	})
}

func TestRouterPrefersInboundEndpoint(t *testing.T) {
	t.Parallel()

	var path atomic.Value
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	provider := testProvider("p", server.URL, ProtocolOpenAI, "/chat/completions", "/responses")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "up", Weight: 1}}}
	router, err := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	cases := []struct {
		inbound WireFormat
		want    string
	}{
		{"openai", "/chat/completions"},
		{"responses", "/responses"},
		{"anthropic", "/chat/completions"},
	}
	for _, test := range cases {
		result, err := send(router, t.Context(), model, textRequest("hi"), nil, "", test.inbound)
		if err != nil {
			t.Fatalf("%s send: %v", test.inbound, err)
		}
		_ = result.Close()
		if got := path.Load().(string); got != test.want {
			t.Fatalf("%s inbound used %q, want %q", test.inbound, got, test.want)
		}
	}
}

func TestRouterFallsBackWhenEndpointMissing(t *testing.T) {
	t.Parallel()

	var path atomic.Value
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})

	provider := testProvider("p", server.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "up"}}}
	router, _ := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	defer router.Close()

	result, err := send(router, t.Context(), model, textRequest("hi"), nil, "", "responses")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = result.Close()
	if got := path.Load().(string); got != "/chat/completions" {
		t.Fatalf("path = %q", got)
	}
}

func TestRouterWeightedStart(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", server.URL, ProtocolOpenAI, "/chat/completions")
	p2 := testProvider("p2", server.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	low, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer low.Close()
	high, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0.999 }), WithSleep(noSleep))
	defer high.Close()

	result, err := send(low, t.Context(), model, textRequest("hi"), nil, "", "openai")
	if err != nil {
		t.Fatalf("low send: %v", err)
	}
	if result.ProviderID != "p1" {
		t.Fatalf("low start = %q", result.ProviderID)
	}
	_ = result.Close()

	result, err = send(high, t.Context(), model, textRequest("hi"), nil, "", "openai")
	if err != nil {
		t.Fatalf("high send: %v", err)
	}
	if result.ProviderID != "p2" {
		t.Fatalf("high start = %q", result.ProviderID)
	}
	_ = result.Close()
}

func TestRouterNetworkFailover(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	live := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", deadURL, ProtocolOpenAI, "/chat/completions")
	p1.MaxRetries = 0
	p2 := testProvider("p2", live.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer router.Close()

	result, err := send(router, t.Context(), model, textRequest("hi"), nil, "", "openai")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if result.ProviderID != "p2" {
		t.Fatalf("provider = %q", result.ProviderID)
	}
}

func TestRouterStickyRouting(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", server.URL, ProtocolOpenAI, "/chat/completions")
	p2 := testProvider("p2", server.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	var mu sync.Mutex
	randomCalls := 0
	source := func() float64 {
		mu.Lock()
		defer mu.Unlock()
		randomCalls++
		if randomCalls == 1 {
			return 0
		}
		return 0.999
	}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(source), WithSleep(noSleep))
	defer router.Close()

	first, err := send(router, t.Context(), model, textRequest("hi"), nil, "req-1", "openai")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	_ = first.Close()

	mu.Lock()
	callsAfterFirst := randomCalls
	mu.Unlock()

	second, err := send(router, t.Context(), model, textRequest("hi"), nil, "req-1", "openai")
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	_ = second.Close()

	if first.ProviderID != "p1" || second.ProviderID != "p1" {
		t.Fatalf("sticky providers = %q, %q", first.ProviderID, second.ProviderID)
	}
	mu.Lock()
	afterSecond := randomCalls
	mu.Unlock()
	if afterSecond != callsAfterFirst {
		t.Fatalf("sticky request consulted the random source %d extra times", afterSecond-callsAfterFirst)
	}
}

func TestRouterRepinsAfterFailover(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	live := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", deadURL, ProtocolOpenAI, "/chat/completions")
	p1.MaxRetries = 0
	p2 := testProvider("p2", live.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer router.Close()

	result, err := send(router, t.Context(), model, textRequest("hi"), nil, "req-1", "openai")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = result.Close()

	router.mu.Lock()
	pinned := router.sticky["req-1"]
	router.mu.Unlock()
	if pinned.providerID != "p2" {
		t.Fatalf("pinned provider = %q", pinned.providerID)
	}
}

func TestRouterDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"missing"}}`))
	})
	other := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	p1 := testProvider("p1", server.URL, ProtocolOpenAI, "/chat/completions")
	p2 := testProvider("p2", other.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer router.Close()

	result, err := send(router, t.Context(), model, textRequest("hi"), nil, "", "openai")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
	if result.ProviderID != "p1" || result.Status != http.StatusNotFound {
		t.Fatalf("result = %+v", result)
	}
	data, _ := result.ReadAll()
	if !strings.Contains(string(data), "missing") {
		t.Fatalf("error body = %q", data)
	}
}

func TestRouterRetriesAndRecordsRetryAfter(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	live := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	p1 := testProvider("p1", server.URL, ProtocolOpenAI, "/chat/completions")
	p1.MaxRetries = 1
	p2 := testProvider("p2", live.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	var sleeps []time.Duration
	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model),
		WithRand(func() float64 { return 0 }),
		WithSleep(func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		}))
	defer router.Close()

	result, err := send(router, t.Context(), model, textRequest("hi"), nil, "", "openai")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
	if result.ProviderID != "p2" {
		t.Fatalf("provider = %q", result.ProviderID)
	}
	if len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("sleeps = %v", sleeps)
	}
}

func TestRouterReturnsLastRetryableError(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
	})
	p := testProvider("p", server.URL, ProtocolOpenAI, "/chat/completions")
	p.MaxRetries = 1
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "u", Weight: 1}}}

	router, _ := NewRouter(testConfig([]*Provider{p}, model), WithSleep(noSleep))
	defer router.Close()

	result, err := send(router, t.Context(), model, textRequest("hi"), nil, "", "openai")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if result.OK() || result.Status != http.StatusServiceUnavailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestRouterAllProvidersFail(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	p := testProvider("p", deadURL, ProtocolOpenAI, "/chat/completions")
	p.MaxRetries = 0
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "u", Weight: 1}}}

	router, _ := NewRouter(testConfig([]*Provider{p}, model), WithSleep(noSleep))
	defer router.Close()

	_, err := send(router, t.Context(), model, textRequest("hi"), nil, "", "openai")
	var gatewayError *util.Error
	if !errors.As(err, &gatewayError) || gatewayError.Status != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterVideoOnlyUsesOpenAIProviders(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	var anthropicHits atomic.Int32
	anthropicServer := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		anthropicHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	openAIProvider := testProvider("p1", deadURL, ProtocolOpenAI, "/chat/completions")
	openAIProvider.MaxRetries = 0
	anthropicProvider := testProvider("p2", anthropicServer.URL, ProtocolAnthropic, "/v1/messages")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	router, _ := NewRouter(testConfig([]*Provider{openAIProvider, anthropicProvider}, model),
		WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer router.Close()

	request := &ir.Request{Messages: []ir.Message{{
		Role: ir.RoleUser,
		Content: []ir.Block{
			ir.TextBlock("describe"),
			{Type: ir.Video, VideoURL: "https://video"},
		},
	}}}
	_, err := send(router, t.Context(), model, request, nil, "", "openai")
	if err == nil {
		t.Fatal("expected failure when the only OpenAI provider is down")
	}
	if anthropicHits.Load() != 0 {
		t.Fatalf("anthropic provider was called %d times", anthropicHits.Load())
	}

	// Without a video block the Anthropic provider is eligible again.
	plain := textRequest("hi")
	result, err := send(router, t.Context(), model, plain, nil, "", "openai")
	if err != nil {
		t.Fatalf("plain send: %v", err)
	}
	defer result.Close()
	if result.ProviderID != "p2" {
		t.Fatalf("provider = %q", result.ProviderID)
	}
}

func TestRouterRawPayloadDropsParamsAndRewritesModel(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		value, err := util.Decode(readAll(t, r))
		if err != nil {
			t.Errorf("decode: %v", err)
		}
		captured, _ = util.AsMap(value)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	provider := testProvider("p", server.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "upstream-chat", Weight: 1}}}
	cfg := testConfig([]*Provider{provider}, model)
	cfg.DropParams = true

	router, _ := NewRouter(cfg, WithSleep(noSleep))
	defer router.Close()

	payload := map[string]any{
		"model":     "public",
		"messages":  []any{map[string]any{"role": "user", "content": "hello"}},
		"stream":    false,
		"litellm_x": 1,
		"_internal": true,
		"nothing":   nil,
	}
	result, err := router.Send(t.Context(), RouteRequest{
		Model:           model,
		PreferredFormat: wireOpenAI,
		Payload:         payload,
	})
	if err != nil {
		t.Fatalf("send raw: %v", err)
	}
	defer result.Close()

	if captured["model"] != "upstream-chat" {
		t.Fatalf("model = %v", captured["model"])
	}
	for _, key := range []string{"litellm_x", "_internal", "nothing"} {
		if _, ok := captured[key]; ok {
			t.Fatalf("dropped key %q forwarded: %v", key, captured)
		}
	}
	if _, ok := captured["messages"]; !ok {
		t.Fatalf("messages dropped: %v", captured)
	}
}

func TestRouterSameWireForwardsPayloadFields(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		value, err := util.Decode(readAll(t, r))
		if err != nil {
			t.Errorf("decode: %v", err)
		}
		captured, _ = util.AsMap(value)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[]}`))
	})

	provider := testProvider("p", server.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "upstream-chat", Weight: 1}}}
	router, _ := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	defer router.Close()

	result, err := router.Send(t.Context(), RouteRequest{
		Model:           model,
		PreferredFormat: wireOpenAI,
		Payload: map[string]any{
			"model":    "public",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"n":        2,
			"user":     "alice",
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()

	if captured["model"] != "upstream-chat" {
		t.Fatalf("model = %v", captured["model"])
	}
	if n, ok := util.AsInt(captured["n"]); !ok || n != 2 {
		t.Fatalf("n = %v", captured["n"])
	}
	if captured["user"] != "alice" {
		t.Fatalf("user = %v", captured["user"])
	}
}

func TestRouterCrossWireConvertsPayload(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		value, err := util.Decode(readAll(t, r))
		if err != nil {
			t.Errorf("decode: %v", err)
		}
		captured, _ = util.AsMap(value)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[]}`))
	})

	provider := testProvider("p", server.URL, ProtocolAnthropic, "/v1/messages")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "claude", Weight: 1}}}
	router, _ := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	defer router.Close()

	result, err := router.Send(t.Context(), RouteRequest{
		Model:           model,
		PreferredFormat: wireOpenAI,
		Payload: map[string]any{
			"model":    "public",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"n":        2,
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()

	if captured["model"] != "claude" {
		t.Fatalf("model = %v", captured["model"])
	}
	if _, ok := captured["messages"]; !ok {
		t.Fatalf("expected Anthropic messages, got %v", captured)
	}
	if _, ok := captured["n"]; ok {
		t.Fatalf("n leaked across protocols: %v", captured)
	}
}

func TestRouterAppliesModelMaxOutputToChatUpstream(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		value, err := util.Decode(readAll(t, r))
		if err != nil {
			t.Errorf("decode: %v", err)
		}
		captured, _ = util.AsMap(value)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	provider := testProvider("p", server.URL, ProtocolOpenAI, "/chat/completions")
	maxOutput := 321
	model := &Model{
		ID:              "m",
		MaxOutputLength: &maxOutput,
		Providers:       []ModelProvider{{ProviderID: "p", UpstreamModel: "up", Weight: 1}},
	}
	router, _ := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	defer router.Close()

	result, err := router.Send(t.Context(), RouteRequest{
		Model:           model,
		PreferredFormat: wireResponses,
		Payload: map[string]any{
			"model": "public",
			"input": "hi",
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()

	if got, ok := util.AsInt(captured["max_tokens"]); !ok || got != maxOutput {
		t.Fatalf("max_tokens = %v, want %d", captured["max_tokens"], maxOutput)
	}
	if _, ok := captured["max_output_tokens"]; ok {
		t.Fatalf("Responses-only max_output_tokens leaked to Chat: %v", captured)
	}
}

func TestRouterContextCancellationStopsRetries(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	p := testProvider("p", deadURL, ProtocolOpenAI, "/chat/completions")
	p.MaxRetries = 3
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "u", Weight: 1}}}

	router, _ := NewRouter(testConfig([]*Provider{p}, model), WithSleep(sleepContext))
	defer router.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := send(router, ctx, model, textRequest("hi"), nil, "", "openai")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterStickyCacheIsBounded(t *testing.T) {
	t.Parallel()

	router, err := NewRouter(&Config{
		Providers:  map[string]*Provider{},
		Models:     map[string]*Model{},
		UsersByKey: map[string]*User{},
	}, WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	for index := 0; index < stickyCacheSize+5; index++ {
		router.pin(fmt.Sprintf("req-%d", index), candidate{
			modelProvider: ModelProvider{ProviderID: "p", UpstreamModel: "u"},
		})
	}
	router.mu.Lock()
	size := router.order.Len()
	entries := len(router.sticky)
	router.mu.Unlock()
	if size != stickyCacheSize || entries != stickyCacheSize {
		t.Fatalf("cache size = %d, entries = %d", size, entries)
	}
}

func TestRouterConcurrentSticky(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", server.URL, ProtocolOpenAI, "/chat/completions")
	p2 := testProvider("p2", server.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithSleep(noSleep))
	defer router.Close()

	var wg sync.WaitGroup
	for index := 0; index < 64; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := send(router, t.Context(), model, textRequest("hi"),
				nil, fmt.Sprintf("req-%d", index%4), "openai")
			if err != nil {
				t.Errorf("send: %v", err)
				return
			}
			_ = result.Close()
		}(index)
	}
	wg.Wait()
}

func TestRouterUpstreamHeaders(t *testing.T) {
	t.Parallel()

	var captured atomic.Value
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		captured.Store(r.Header.Clone())
		w.WriteHeader(http.StatusOK)
	})

	openAI := testProvider("openai", server.URL, ProtocolOpenAI, "/chat/completions")
	openAI.Key = "sk-test"
	openAI.Headers = map[string]string{"x-extra": "1", "Authorization": "Custom token"}

	anthropicProvider := testProvider("anthropic", server.URL, ProtocolAnthropic, "/v1/messages")
	anthropicProvider.Key = "ak-test"

	modelFor := func(providerID string) *Model {
		return &Model{ID: "m", Providers: []ModelProvider{{ProviderID: providerID, UpstreamModel: "u", Weight: 1}}}
	}

	openAIModel := modelFor("openai")
	openAIRouter, _ := NewRouter(testConfig([]*Provider{openAI}, openAIModel), WithSleep(noSleep))
	defer openAIRouter.Close()
	result, err := send(openAIRouter, t.Context(), openAIModel, textRequest("hi"), nil, "", "openai")
	if err != nil {
		t.Fatalf("openai send: %v", err)
	}
	_ = result.Close()

	headers := captured.Load().(http.Header)
	if headers.Get("Authorization") != "Custom token" {
		t.Fatalf("provider headers must win: %q", headers.Get("Authorization"))
	}
	if headers.Get("x-extra") != "1" {
		t.Fatalf("extra header missing: %v", headers)
	}
	if headers.Get("User-Agent") != "llm-proxy/"+Version {
		t.Fatalf("user agent = %q", headers.Get("User-Agent"))
	}
	if headers.Get("Accept") != "application/json" {
		t.Fatalf("accept = %q", headers.Get("Accept"))
	}

	anthropicModel := modelFor("anthropic")
	anthropicRouter, _ := NewRouter(testConfig([]*Provider{anthropicProvider}, anthropicModel), WithSleep(noSleep))
	defer anthropicRouter.Close()
	result, err = send(anthropicRouter, t.Context(), anthropicModel, textRequest("hi"),
		map[string]string{"anthropic-version": "2024-01-01", "anthropic-beta": "beta-1"}, "", "openai")
	if err != nil {
		t.Fatalf("anthropic send: %v", err)
	}
	_ = result.Close()

	headers = captured.Load().(http.Header)
	if headers.Get("x-api-key") != "ak-test" {
		t.Fatalf("x-api-key = %q", headers.Get("x-api-key"))
	}
	if headers.Get("anthropic-version") != "2024-01-01" || headers.Get("anthropic-beta") != "beta-1" {
		t.Fatalf("anthropic headers = %v", headers)
	}
	if headers.Get("Authorization") != "" {
		t.Fatalf("unexpected authorization header: %q", headers.Get("Authorization"))
	}

	// A stream request advertises SSE.
	result, err = send(anthropicRouter, t.Context(), anthropicModel,
		&ir.Request{Stream: true, Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Block{ir.TextBlock("hi")}}}},
		nil, "", "openai")
	if err != nil {
		t.Fatalf("stream send: %v", err)
	}
	_ = result.Close()
	headers = captured.Load().(http.Header)
	if headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("stream accept = %q", headers.Get("Accept"))
	}
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

func TestRetryDelayClampsHugeAttempt(t *testing.T) {
	t.Parallel()

	if got := retryDelay(1000, nil); got != maxBackoff {
		t.Fatalf("retryDelay = %v, want %v", got, maxBackoff)
	}
}

func TestRouterPreservesPrepareClientError(t *testing.T) {
	t.Parallel()

	provider := testProvider("p", "https://example.test", ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p", UpstreamModel: "u", Weight: 1},
	}}
	router, err := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	_, err = router.dispatch(t.Context(), RouteRequest{
		Model:           model,
		PreferredFormat: wireOpenAI,
	}, func(candidate) (map[string]any, error) {
		return nil, util.New(400, "invalid body", "", "invalid_body", nil)
	}, false)
	var gatewayError *util.Error
	if !errors.As(err, &gatewayError) {
		t.Fatalf("error = %v, want *util.Error", err)
	}
	if gatewayError.Status != 400 || gatewayError.Code != "invalid_body" {
		t.Fatalf("error = %+v", gatewayError)
	}
}
