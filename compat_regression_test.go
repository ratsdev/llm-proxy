package llmproxy

// Compatibility regression tests for routing and HTTP behaviour. They encode
// expectations taken from other gateways (litellm, bifrost) and from the
// upstream APIs themselves.

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

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

	busyProvider := testProvider("p1", busy.URL, ProtocolAnthropic, "/v1/messages")
	busyProvider.MaxRetries = 0
	liveProvider := testProvider("p2", live.URL, ProtocolOpenAI, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1", Weight: 1},
		{ProviderID: "p2", UpstreamModel: "u2", Weight: 1},
	}}

	router, err := NewRouter(testConfig([]*Provider{busyProvider, liveProvider}, model),
		WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	result, err := send(router, t.Context(), model, textRequest("hi"), nil, "", "openai")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if result.ProviderID != "p2" {
		t.Fatalf("529 was not retried/failed over: provider=%q status=%d", result.ProviderID, result.Status)
	}
}

// Rate-limited clients back off using Retry-After. The gateway consumes the
// upstream response, so it must relay that header instead of dropping it.
func TestCompatForwardsRetryAfter(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    "openai",
				Status:        http.StatusTooManyRequests,
				Header:        http.Header{"Retry-After": []string{"7"}},
				Body:          io.NopCloser(strings.NewReader(`{"error":{"message":"slow down","type":"rate_limit_error"}}`)),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After = %q, want \"7\"", got)
	}
}

// If an upstream answers a stream request with a normal JSON body (some
// OpenAI-compatible providers ignore "stream"), the caller must still receive
// the completion, not a fabricated empty stream.
func TestCompatStreamingUpstreamJSONBody(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return staticResult(model, http.StatusOK, `{
				"id":"chatcmpl-up","model":"model-a","created":7,
				"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":2,"completion_tokens":1}
			}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("stream lost the upstream completion: %s", data)
	}
}

// Fields the gateway has no IR representation for must not be lost when the
// inbound and outbound wire formats are identical; callers rely on
// system_fingerprint, logprobs and similar passthrough data.
func TestCompatSameProtocolKeepsResponseExtras(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return staticResult(model, http.StatusOK, `{
				"id":"chatcmpl-up","object":"chat.completion","model":"model-a","created":7,
				"system_fingerprint":"fp_1",
				"choices":[{"index":0,"logprobs":{"content":[{"token":"hello","logprob":-0.1}]},
					"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":2,"completion_tokens":1}
			}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	body := decodeBody(t, response)

	if body["system_fingerprint"] != "fp_1" {
		t.Fatalf("system_fingerprint dropped: %v", body)
	}
	choices, _ := body["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("choices = %v", body["choices"])
	}
	if _, ok := choices[0].(map[string]any)["logprobs"]; !ok {
		t.Fatalf("logprobs dropped: %v", choices[0])
	}
}

// Same-protocol streaming must also preserve fields the IR does not model,
// while still rewriting the public model name.
func TestCompatSameProtocolStreamKeepsUpstreamFields(t *testing.T) {
	t.Parallel()

	stream := "data: {\"id\":\"chatcmpl-up\",\"object\":\"chat.completion.chunk\",\"model\":\"up\"," +
		"\"created\":7,\"system_fingerprint\":\"fp_1\"," +
		"\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-up\",\"model\":\"up\"," +
		"\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	router := &fakeRouter{
		send: func(model *Model, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireOpenAI,
				Status:        http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:          io.NopCloser(strings.NewReader(stream)),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "system_fingerprint") {
		t.Fatalf("system_fingerprint dropped: %s", output)
	}
	if !strings.Contains(output, `"model":"model/a"`) {
		t.Fatalf("model not rewritten: %s", output)
	}
	if !strings.Contains(output, `"content":"hi"`) || !strings.Contains(output, "[DONE]") {
		t.Fatalf("stream mangled: %s", output)
	}
}

// The passthrough model rewrite has to reach the nested model fields used by
// the Anthropic and Responses wire formats.
func TestCompatPassthroughRewritesNestedModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
	}{
		{
			name: "anthropic message_start",
			line: `data: {"type":"message_start","message":{"id":"msg_1","model":"up"}}`,
		},
		{
			name: "responses created",
			line: `data: {"type":"response.created","response":{"id":"resp_1","model":"up"}}`,
		},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rewritten, ok := rewriteStreamModel([]byte(test.line+"\n"), "public")
			if !ok {
				t.Fatalf("line was not rewritten: %s", test.line)
			}
			if !strings.Contains(string(rewritten), `"model":"public"`) {
				t.Fatalf("nested model not rewritten: %s", rewritten)
			}
		})
	}
}

// A Chat request for multiple choices must keep "n" on the way out and all
// returned choices on the way back (passthrough path).
func TestCompatSameProtocolKeepsMultipleChoices(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{}
	router.send = func(model *Model, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
		n, ok := util.AsInt(router.lastPayload["n"])
		if !ok || n != 2 {
			t.Errorf("n not forwarded: %+v", router.lastPayload)
		}
		return staticResult(model, http.StatusOK, `{
				"id":"chatcmpl-up","object":"chat.completion","model":"model-a","created":7,
				"choices":[
					{"index":0,"message":{"role":"assistant","content":"one"},"finish_reason":"stop"},
					{"index":1,"message":{"role":"assistant","content":"two"},"finish_reason":"stop"}
				],
				"usage":{"prompt_tokens":2,"completion_tokens":2}
			}`), nil
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","n":2,"messages":[{"role":"user","content":"hi"}]}`)
	body := decodeBody(t, response)

	choices, _ := body["choices"].([]any)
	if len(choices) != 2 {
		t.Fatalf("choices = %v", body["choices"])
	}
	if body["model"] != "model/a" {
		t.Fatalf("model = %v", body["model"])
	}
}
