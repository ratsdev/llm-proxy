package gateway

// Compatibility regression tests for routing and HTTP behaviour. They encode
// expectations taken from other gateways (litellm, bifrost) and from the
// upstream APIs themselves.

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// Anthropic reports overload as HTTP 529; the routing engine retries it and
// fails over. See internal/router/compat_test.go for that assertion.

// Rate-limited clients back off using Retry-After. The gateway consumes the
// upstream response, so it must relay that header instead of dropping it.
func TestCompatForwardsRetryAfter(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
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
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
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

// A non-streaming caller whose upstream answers with SSE must still receive
// that SSE stream unchanged. The gateway never re-frames or synthesises an
// event stream.
func TestCompatNonStreamClientReceivesUpstreamSSE(t *testing.T) {
	t.Parallel()

	const stream = "data: {\"id\":\"chunk\",\"model\":\"model-a\",\"choices\":[]}\n\n" +
		"data: {\"id\":\"chunk\",\"model\":\"model-a\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\n" +
		"data: [DONE]\n\n"
	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
				Status:        http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:          io.NopCloser(strings.NewReader(stream)),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if contentType := response.Header.Get("content-type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("content-type = %q", contentType)
	}
	body := string(data)
	if strings.Count(body, "data:") != 3 || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream was not relayed verbatim: %s", body)
	}
	if strings.Contains(body, "model-a") || !strings.Contains(body, "model/a") {
		t.Fatalf("stream model was not rewritten: %s", body)
	}
}

// Non-stream JSON replies are relayed byte-for-byte apart from the model name,
// while usage is still extracted for the dashboard.
func TestCompatNonStreamPassthroughPreservesBodyAndUsage(t *testing.T) {
	t.Parallel()

	const upstreamBody = `{"z":1,  "model":"model-a", "usage":{"prompt_tokens":5,"completion_tokens":2}, "choices":[]}`
	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return staticResult(model, http.StatusOK, upstreamBody), nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := strings.Replace(upstreamBody, `"model-a"`, `"model/a"`, 1)
	if string(data) != want {
		t.Fatalf("body = %s, want %s", data, want)
	}

	admin := uiLogin(t, server)
	requests := uiDecode(t, uiCall(t, admin, http.MethodGet, server.URL+"/api/ui/requests", "", nil))
	records, _ := requests["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("records = %v", requests)
	}
	record, _ := records[0].(map[string]any)
	if record["input_tokens"] != float64(5) || record["output_tokens"] != float64(2) {
		t.Fatalf("usage not recorded: %v", record)
	}
}

// Fields the gateway has no IR representation for must not be lost when the
// inbound and outbound wire formats are identical; callers rely on
// system_fingerprint, logprobs and similar passthrough data.
func TestCompatSameProtocolKeepsResponseExtras(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
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
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
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
	router.send = func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
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
