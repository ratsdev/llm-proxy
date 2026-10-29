package router

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/core"
	"github.com/ratsdev/llm-proxy/internal/util"
)

func TestRouterUsesSharedBaseURLAndEndpointPath(t *testing.T) {
	t.Parallel()

	var (
		gotPath    atomic.Value
		gotPayload atomic.Value
	)
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		data, _ := io.ReadAll(r.Body)
		gotPayload.Store(string(data))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	provider := &Provider{
		ID:         "p",
		BaseURL:    server.URL,
		MaxRetries: 1,
		Headers:    map[string]string{},
		Endpoints: map[WireFormat]ProviderEndpoint{
			wireChatCompletions: {Path: "/custom/chat"},
		},
	}
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "up"}}}
	router, err := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", wireChatCompletions)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()

	if gotPath.Load() != "/custom/chat" {
		t.Fatalf("upstream path = %v", gotPath.Load())
	}
	if payload, _ := gotPayload.Load().(string); !strings.Contains(payload, `"model":"up"`) {
		t.Fatalf("upstream payload = %s", payload)
	}
}

// A base URL may carry a query string. Appending the endpoint path to the URL
// text would move the path into the query, so URLFor must join the path.
func TestProviderURLForKeepsBaseQuery(t *testing.T) {
	t.Parallel()

	provider := &Provider{BaseURL: "https://api.example.com/v1?api-version=2024-06"}
	got := provider.URLFor(ProviderEndpoint{Path: "/chat/completions"})
	want := "https://api.example.com/v1/chat/completions?api-version=2024-06"
	if got != want {
		t.Fatalf("URLFor = %q, want %q", got, want)
	}
}

func TestRouterEndpointProtocolMustBeConfigured(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	// Only the Responses endpoint is configured, so a Chat Completions request
	// must not silently fall back to it.
	provider := &Provider{
		ID:         "p",
		BaseURL:    server.URL,
		MaxRetries: 1,
		Headers:    map[string]string{},
		Endpoints: map[WireFormat]ProviderEndpoint{
			wireResponses: {Path: "/responses", Converter: convert.None},
		},
	}
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "up"}}}
	router, err := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	_, err = send(t.Context(), router, model, textRequest("hi"), nil, "", wireChatCompletions)
	var gatewayError *util.Error
	if !errors.As(err, &gatewayError) {
		t.Fatalf("err = %v, want *util.Error", err)
	}
	if gatewayError.Code != "unsupported_conversion" {
		t.Fatalf("error = %+v", gatewayError)
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream was called %d times", hits.Load())
	}
}

func TestRouterPrefersInboundEndpoint(t *testing.T) {
	t.Parallel()

	var path atomic.Value
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	provider := testProvider("p", server.URL, "/chat/completions", "/responses")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "up"}}}
	router, err := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	cases := []struct {
		inbound WireFormat
		want    string
	}{
		{"chat_completions", "/chat/completions"},
		{"responses", "/responses"},
	}
	for _, test := range cases {
		result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", test.inbound)
		if err != nil {
			t.Fatalf("%s send: %v", test.inbound, err)
		}
		_ = result.Close()
		if got := path.Load().(string); got != test.want {
			t.Fatalf("%s inbound used %q, want %q", test.inbound, got, test.want)
		}
	}
}

func TestRouterRejectsWireWithoutConverter(t *testing.T) {
	t.Parallel()

	var path atomic.Value
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})

	provider := testProvider("p", server.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "up"}}}
	router, _ := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	defer router.Close()

	_, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "responses")
	var gatewayError *util.Error
	if !errors.As(err, &gatewayError) {
		t.Fatalf("err = %v, want *util.Error", err)
	}
	if gatewayError.Status != http.StatusBadRequest || gatewayError.Code != "unsupported_conversion" {
		t.Fatalf("error = %+v", gatewayError)
	}
	if path.Load() != nil {
		t.Fatalf("upstream was called for an unsupported conversion")
	}
}

func TestRouterRandomStrategy(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", server.URL, "/chat/completions")
	p2 := testProvider("p2", server.URL, "/chat/completions")
	model := &Model{ID: "m", Routing: RoutingRandom, Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	low, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer low.Close()
	high, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0.999 }), WithSleep(noSleep))
	defer high.Close()

	result, err := send(t.Context(), low, model, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("low send: %v", err)
	}
	if result.ProviderID != "p2" {
		t.Fatalf("low start = %q", result.ProviderID)
	}
	_ = result.Close()

	result, err = send(t.Context(), high, model, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("high send: %v", err)
	}
	if result.ProviderID != "p1" {
		t.Fatalf("high start = %q", result.ProviderID)
	}
	_ = result.Close()
}

func TestRouterPriorityStrategyKeepsDeclaredOrder(t *testing.T) {
	t.Parallel()

	var p1Calls, p2Calls atomic.Int32
	p1Server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		p1Calls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	p2Server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		p2Calls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	p1 := testProvider("p1", p1Server.URL, "/chat/completions")
	p2 := testProvider("p2", p2Server.URL, "/chat/completions")
	model := &Model{ID: "m", Routing: RoutingPriority, Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithSleep(noSleep))
	defer router.Close()

	for _, stickyID := range []string{"s1", "s2", "s3"} {
		result, err := send(t.Context(), router, model, textRequest("hi"), nil, stickyID, "chat_completions")
		if err != nil {
			t.Fatalf("send %s: %v", stickyID, err)
		}
		if result.ProviderID != "p1" {
			t.Fatalf("send %s provider = %q", stickyID, result.ProviderID)
		}
		_ = result.Close()
	}
	if p1Calls.Load() != 3 || p2Calls.Load() != 0 {
		t.Fatalf("calls = %d/%d", p1Calls.Load(), p2Calls.Load())
	}
}

func TestRouterRoundRobinStrategyRotates(t *testing.T) {
	t.Parallel()

	server := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", server.URL, "/chat/completions")
	p2 := testProvider("p2", server.URL, "/chat/completions")
	model := &Model{ID: "m", Routing: RoutingRoundRobin, Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithSleep(noSleep))
	defer router.Close()

	got := make([]string, 0, 3)
	for index := 0; index < 3; index++ {
		result, err := send(t.Context(), router, model, textRequest("hi"), nil,
			fmt.Sprintf("session-%d", index), "chat_completions")
		if err != nil {
			t.Fatalf("send %d: %v", index, err)
		}
		got = append(got, result.ProviderID)
		_ = result.Close()
	}
	if strings.Join(got, ",") != "p1,p2,p1" {
		t.Fatalf("round robin order = %v", got)
	}
}

func TestRouterNetworkFailover(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	live := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	p1 := testProvider("p1", deadURL, "/chat/completions")
	p1.MaxRetries = 0
	p2 := testProvider("p2", live.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, _ := NewRouter(testConfig([]*Provider{p1, p2}, model), WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if result.ProviderID != "p2" {
		t.Fatalf("provider = %q", result.ProviderID)
	}
}

func TestRouterAllProvidersFail(t *testing.T) {
	t.Parallel()

	dead := upstream(t, func(w http.ResponseWriter, r *http.Request) {})
	deadURL := dead.URL
	dead.Close()

	p := testProvider("p", deadURL, "/chat/completions")
	p.MaxRetries = 0
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "u"}}}

	router, _ := NewRouter(testConfig([]*Provider{p}, model), WithSleep(noSleep))
	defer router.Close()

	_, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "chat_completions")
	var gatewayError *util.Error
	if !errors.As(err, &gatewayError) || gatewayError.Status != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
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

	provider := testProvider("p", server.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "upstream-chat"}}}
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
		PreferredFormat: wireChatCompletions,
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

	provider := testProvider("p", server.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "upstream-chat"}}}
	router, _ := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	defer router.Close()

	result, err := router.Send(t.Context(), RouteRequest{
		Model:           model,
		PreferredFormat: wireChatCompletions,
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

func TestRouterRejectsCrossWirePayload(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	provider := testProvider("p", server.URL, "/v1/messages")
	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "claude"}}}
	router, _ := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	defer router.Close()

	_, err := router.Send(t.Context(), RouteRequest{
		Model:           model,
		PreferredFormat: wireChatCompletions,
		Payload: map[string]any{
			"model":    "public",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"n":        2,
		},
	})
	var gatewayError *util.Error
	if !errors.As(err, &gatewayError) {
		t.Fatalf("err = %v, want *util.Error", err)
	}
	if gatewayError.Status != http.StatusBadRequest || gatewayError.Code != "unsupported_conversion" {
		t.Fatalf("error = %+v", gatewayError)
	}
	if hits.Load() != 0 {
		t.Fatalf("cross-wire request reached the upstream %d times", hits.Load())
	}
}

func TestRouterPreservesPrepareClientError(t *testing.T) {
	t.Parallel()

	provider := testProvider("p", "https://example.test", "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p", UpstreamModel: "u"},
	}}
	router, err := NewRouter(testConfig([]*Provider{provider}, model), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	_, err = router.dispatch(t.Context(), RouteRequest{
		Model:           model,
		PreferredFormat: wireChatCompletions,
	}, func(candidate) (map[string]any, error) {
		return nil, util.New(400, "invalid body", "", "invalid_body", nil)
	})
	var gatewayError *util.Error
	if !errors.As(err, &gatewayError) {
		t.Fatalf("error = %v, want *util.Error", err)
	}
	if gatewayError.Status != 400 || gatewayError.Code != "invalid_body" {
		t.Fatalf("error = %+v", gatewayError)
	}
}

// Upstream request headers.

func TestRouterUpstreamHeaders(t *testing.T) {
	t.Parallel()

	var captured atomic.Value
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		captured.Store(r.Header.Clone())
		w.WriteHeader(http.StatusOK)
	})

	openAI := testProvider("openai", server.URL, "/chat/completions")
	openAI.APIKey = "sk-test"
	openAI.Headers = map[string]string{"x-extra": "1", "Authorization": "Custom token"}

	anthropicProvider := testProvider("anthropic", server.URL, "/v1/messages")
	anthropicProvider.APIKey = "ak-test"

	modelFor := func(providerID string) *Model {
		return &Model{ID: "m", Providers: []ModelProvider{{ProviderID: providerID, UpstreamModel: "u"}}}
	}

	openAIModel := modelFor("openai")
	openAIRouter, _ := NewRouter(testConfig([]*Provider{openAI}, openAIModel), WithSleep(noSleep))
	defer openAIRouter.Close()
	result, err := send(t.Context(), openAIRouter, openAIModel, textRequest("hi"), nil, "", "chat_completions")
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
	if headers.Get("User-Agent") != "llm-proxy/"+core.Value {
		t.Fatalf("user agent = %q", headers.Get("User-Agent"))
	}
	if headers.Get("Accept") != "application/json" {
		t.Fatalf("accept = %q", headers.Get("Accept"))
	}

	anthropicModel := modelFor("anthropic")
	anthropicRouter, _ := NewRouter(testConfig([]*Provider{anthropicProvider}, anthropicModel), WithSleep(noSleep))
	defer anthropicRouter.Close()
	result, err = send(t.Context(), anthropicRouter, anthropicModel, textRequest("hi"),
		map[string]string{"anthropic-version": "2024-01-01", "anthropic-beta": "beta-1"}, "", "messages")
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
	streamPayload := textRequest("hi")
	streamPayload["stream"] = true
	result, err = send(t.Context(), anthropicRouter, anthropicModel, streamPayload, nil, "", "messages")
	if err != nil {
		t.Fatalf("stream send: %v", err)
	}
	_ = result.Close()
	headers = captured.Load().(http.Header)
	if headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("stream accept = %q", headers.Get("Accept"))
	}
}

// Compatibility regressions.

// Anthropic reports overload as HTTP 529. Gateways retry it and fail over
// because it is a provider-side capacity error, not a client error.
func TestCompatRetriesOverloaded529(t *testing.T) {
	t.Parallel()

	busy := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	})
	live := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	busyProvider := testProvider("p1", busy.URL, "/chat/completions")
	busyProvider.MaxRetries = 0
	liveProvider := testProvider("p2", live.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, err := NewRouter(testConfig([]*Provider{busyProvider, liveProvider}, model),
		WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if result.ProviderID != "p2" {
		t.Fatalf("529 was not retried/failed over: provider=%q status=%d", result.ProviderID, result.Status)
	}
}
