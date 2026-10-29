package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"

	routing "github.com/ratsdev/llm-proxy/internal/router"
	"github.com/ratsdev/llm-proxy/internal/util"
)

func TestServerChatRewritesModelAndSetsHeaders(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			// The upstream replies with the upstream model name and an id.
			return staticResult(model, http.StatusOK, `{
				"id":"chatcmpl-up","model":"model-a","created":7,
				"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":2,"completion_tokens":1}
			}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response.Header.Get("content-type") != "application/json" {
		t.Fatalf("content type = %q", response.Header.Get("content-type"))
	}
	if response.Header.Get("x-provider-id") != "provider" {
		t.Fatalf("provider header = %q", response.Header.Get("x-provider-id"))
	}

	body := decodeBody(t, response)
	if body["model"] != "model/a" {
		t.Fatalf("model = %v", body["model"])
	}
	choices := body["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "hello" {
		t.Fatalf("message = %v", message)
	}
	if body["id"] != "chatcmpl-up" {
		t.Fatalf("id = %v", body["id"])
	}

	router.mu.Lock()
	format := router.lastProtocol
	router.mu.Unlock()
	if format != wireChatCompletions {
		t.Fatalf("dispatch format = %q", format)
	}
}

func TestServerForwardsAnthropicHeaders(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{}
	server := newTestServer(t, router, nil)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/messages",
		strings.NewReader(`{"model":"model/a","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "key-a")
	request.Header.Set("anthropic-version", "2024-01-01")
	request.Header.Set("anthropic-beta", "beta-1")
	request.Header.Set("x-request-id", "client-42")
	request.Header.Set("x-session-id", "conv-42")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	router.mu.Lock()
	inbound := router.lastInboundHeads
	stickyID := router.lastStickyID
	format := router.lastProtocol
	router.mu.Unlock()

	if format != wireMessages {
		t.Fatalf("format = %q", format)
	}
	if inbound["anthropic-version"] != "2024-01-01" || inbound["anthropic-beta"] != "beta-1" {
		t.Fatalf("forwarded headers = %v", inbound)
	}
	if stickyID != routing.SessionDigest("model/a", "alice", "x-session-id=conv-42") {
		t.Fatalf("sticky id = %q", stickyID)
	}
}

func TestServerUpstreamInvalidJSON(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return staticResult(model, http.StatusOK, `[1,2,3]`), nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if decodeBody(t, response)["error"].(map[string]any)["code"] != "upstream_error" {
		t.Fatal("expected upstream_error")
	}
}

func TestServerRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	huge := `{"model":"model/a","messages":[{"role":"user","content":"` +
		strings.Repeat("x", int(maxRequestBodyBytes)+1024) + `"}]}`
	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a", huge)
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestServerEmbeddedJSONErrorObject(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return staticResult(model, http.StatusOK,
				`{"error":{"message":"boom","type":"server_error"}}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	// A 200 reply that embeds an error must not be mistaken for success.
	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", response.StatusCode)
	}
	data, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(data), "boom") {
		t.Fatalf("error body = %s", data)
	}
}

// usageStream returns a Chat Completions SSE reply shaped like the one an
// upstream sends when stream_options.include_usage is set.
func usageStream() *RouteResult {
	return &RouteResult{
		ProviderID:    "provider",
		UpstreamModel: "model-a",
		WireFormat:    wireChatCompletions,
		Status:        http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"c\",\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
				"data: {\"id\":\"c\",\"model\":\"model-a\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":5}}\n\n" +
				"data: [DONE]\n\n")),
	}
}

// The gateway asks a Chat Completions upstream for the trailing usage chunk so
// the console can report throughput, then hides it from a caller that never
// requested usage.
func TestServerRequestsStreamUsageAndHidesTheChunk(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return usageStream(), nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body := readBody(t, response)

	router.mu.Lock()
	sent := router.lastPayload
	router.mu.Unlock()
	options, _ := util.AsMap(sent["stream_options"])
	if !util.Truthy(options["include_usage"]) {
		t.Fatalf("upstream payload = %v", sent)
	}
	if strings.Contains(body, "usage") {
		t.Fatalf("usage chunk leaked to the caller: %s", body)
	}
	if strings.Count(body, "data:") != 2 {
		t.Fatalf("stream was not filtered: %s", body)
	}
}

// A caller that asked for usage still receives the chunk the gateway requested.
func TestServerKeepsRequestedStreamUsage(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return usageStream(), nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"stream_options":{"include_usage":true},`+
			`"messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if body := readBody(t, response); !strings.Contains(body, `"usage"`) {
		t.Fatalf("requested usage chunk was dropped: %s", body)
	}
}
