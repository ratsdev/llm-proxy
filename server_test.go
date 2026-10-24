package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ratsdev/llm-proxy/ir"
)

type fakeRouter struct {
	mu sync.Mutex

	send func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error)
	raw  func(model *Model, operation string, payload map[string]any, stickyID string) (*RouteResult, error)

	lastStickyID     string
	lastProtocol     WireFormat
	lastOperation    string
	lastInboundHeads map[string]string
	lastPayload      map[string]any
}

func (f *fakeRouter) Send(_ context.Context, request RouteRequest) (*RouteResult, error) {
	f.mu.Lock()
	f.lastStickyID = request.StickyID
	f.lastOperation = request.Operation
	f.lastInboundHeads = request.InboundHeaders
	f.lastProtocol = request.PreferredFormat
	f.lastPayload = request.Payload
	f.mu.Unlock()

	if !request.ChatFamily {
		if f.raw == nil {
			return staticResult(request.Model, http.StatusOK, `{"data":[]}`), nil
		}
		return f.raw(request.Model, request.Operation, request.Payload, request.StickyID)
	}
	if f.send == nil {
		return staticResult(request.Model, http.StatusOK, `{"ok":true}`), nil
	}
	return f.send(request.Model, request.Operation, request.Request, request.StickyID, string(request.PreferredFormat))
}

func staticResult(model *Model, status int, body string) *RouteResult {
	return &RouteResult{
		ProviderID:    "provider",
		UpstreamModel: firstUpstream(model),
		WireFormat:    wireOpenAI,
		Status:        status,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
	}
}

func errorResult(model *Model, status int, body string) *RouteResult {
	return &RouteResult{
		ProviderID:    "provider",
		UpstreamModel: firstUpstream(model),
		WireFormat:    wireOpenAI,
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

func newTestServer(t *testing.T, router routerAPI, mutate func(*Config)) *httptest.Server {
	t.Helper()
	cfg := &Config{
		Providers: map[string]*Provider{
			"provider": {ID: "provider", BaseURL: "http://upstream", Protocol: ProtocolOpenAI,
				MaxRetries: 0, Endpoints: []string{"/chat/completions"}, Headers: map[string]string{}},
		},
		Models: map[string]*Model{
			"model/a": {ID: "model/a", Modalities: []string{"text"},
				Providers: []ModelProvider{{ProviderID: "provider", UpstreamModel: "model-a", Weight: 1}}},
			"model-b": {ID: "model-b", Modalities: []string{"text"},
				Providers: []ModelProvider{{ProviderID: "provider", UpstreamModel: "model-b", Weight: 1}}},
		},
		UsersByKey: map[string]*User{
			"key-a": {ID: "alice", APIKey: "key-a", AllowedModels: []string{"model/a"}},
			"key-b": {ID: "bob", APIKey: "key-b"},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	server := NewServer(cfg, router, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func TestServerIndexAndHealth(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)

	response := doJSON(t, http.MethodGet, server.URL+"/api/", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	if body["name"] != "llm-proxy" || body["version"] != Version {
		t.Fatalf("index = %v", body)
	}
	if got, _ := body["models"].(float64); int(got) != 2 {
		t.Fatalf("models = %v", body["models"])
	}
	endpoints, _ := body["endpoints"].(map[string]any)
	openai, _ := endpoints["openai"].([]any)
	if len(openai) == 0 || openai[0] != "/api/v1/chat/completions" {
		t.Fatalf("advertised openai endpoints = %v", endpoints["openai"])
	}

	bare := doJSON(t, http.MethodGet, server.URL+"/api", "", "")
	if bare.StatusCode != http.StatusOK {
		t.Fatalf("/api status = %d", bare.StatusCode)
	}

	root := doJSON(t, http.MethodGet, server.URL+"/", "", "")
	if root.StatusCode != http.StatusNotFound {
		t.Fatalf("root status = %d, want 404", root.StatusCode)
	}

	response = doJSON(t, http.MethodGet, server.URL+"/api/health", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	body = decodeBody(t, response)
	if body["status"] != "healthy" {
		t.Fatalf("health body = %v", body)
	}
	if got, _ := body["providers"].(float64); int(got) != 1 {
		t.Fatalf("health providers = %v", body["providers"])
	}
}

func TestServerAuthentication(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	errorObject := body["error"].(map[string]any)
	if errorObject["code"] != "missing_api_key" || errorObject["type"] != "authentication_error" {
		t.Fatalf("error = %v", errorObject)
	}

	invalid := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "unknown",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if invalid.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid status = %d", invalid.StatusCode)
	}
	if decodeBody(t, invalid)["error"].(map[string]any)["code"] != "invalid_api_key" {
		t.Fatal("expected invalid_api_key")
	}

	// x-api-key also authenticates.
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/chat/completions",
		strings.NewReader(`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "key-a")
	response2, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response2.Body.Close()
	if response2.StatusCode != http.StatusOK {
		t.Fatalf("x-api-key status = %d", response2.StatusCode)
	}
}

func TestServerAuthDisabledWithoutUsers(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		cfg.UsersByKey = map[string]*User{}
	})
	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestServerModelListingRespectsACL(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)

	restricted := decodeBody(t, doJSON(t, http.MethodGet, server.URL+"/api/v1/models", "key-a", ""))
	data := restricted["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["id"] != "model/a" {
		t.Fatalf("restricted models = %v", data)
	}

	unrestricted := decodeBody(t, doJSON(t, http.MethodGet, server.URL+"/api/v1/models", "key-b", ""))
	if len(unrestricted["data"].([]any)) != 2 {
		t.Fatalf("unrestricted models = %v", unrestricted["data"])
	}

	detail := doJSON(t, http.MethodGet, server.URL+"/api/v1/models/model/a", "key-a", "")
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d", detail.StatusCode)
	}
	detailBody := decodeBody(t, detail)
	if detailBody["id"] != "model/a" || detailBody["object"] != "model" {
		t.Fatalf("detail = %v", detailBody)
	}

	forbidden := doJSON(t, http.MethodGet, server.URL+"/api/v1/models/model-b", "key-a", "")
	if forbidden.StatusCode != http.StatusForbidden {
		t.Fatalf("forbidden status = %d", forbidden.StatusCode)
	}
	if decodeBody(t, forbidden)["error"].(map[string]any)["code"] != "model_not_allowed" {
		t.Fatal("expected model_not_allowed")
	}

	missing := doJSON(t, http.MethodGet, server.URL+"/api/v1/models/unknown", "key-b", "")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.StatusCode)
	}
}

func TestServerRequestErrors(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)

	cases := []struct {
		name       string
		body       string
		path       string
		wantStatus int
		wantCode   string
	}{
		{"invalid json", `{`, "/api/v1/chat/completions", 400, "invalid_json"},
		{"not an object", `[1]`, "/api/v1/chat/completions", 400, "invalid_body"},
		{"missing model", `{}`, "/api/v1/chat/completions", 400, "model_required"},
		{"blank model", `{"model":"  "}`, "/api/v1/chat/completions", 400, "model_required"},
		{"unknown model", `{"model":"nope"}`, "/api/v1/chat/completions", 404, "model_not_found"},
		{"denied model", `{"model":"model-b"}`, "/api/v1/chat/completions", 403, "model_not_allowed"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := doJSON(t, http.MethodPost, server.URL+test.path, "key-a", test.body)
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d", response.StatusCode)
			}
			errorObject := decodeBody(t, response)["error"].(map[string]any)
			if errorObject["code"] != test.wantCode {
				t.Fatalf("code = %v", errorObject["code"])
			}
		})
	}
}

func TestServerPreservesClientRequestID(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return errorResult(model, http.StatusTooManyRequests, `{"error":{"message":"slow down","type":"rate_limit_error"}}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/chat/completions",
		strings.NewReader(`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer key-a")
	request.Header.Set("x-request-id", "client-req_1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("x-request-id"); got != "client-req_1" {
		t.Fatalf("request id = %q", got)
	}
	if got := response.Header.Get("x-provider-id"); got != "provider" {
		t.Fatalf("provider header = %q", got)
	}
	if got := response.Header.Get("x-upstream-model"); got != "model-a" {
		t.Fatalf("upstream header = %q", got)
	}
	errorObject := decodeBody(t, response)["error"].(map[string]any)
	if errorObject["message"] != "slow down" || errorObject["type"] != "rate_limit_error" {
		t.Fatalf("error = %v", errorObject)
	}

	router.mu.Lock()
	stickyID := router.lastStickyID
	router.mu.Unlock()
	if stickyID != "client-req_1" {
		t.Fatalf("sticky id = %q", stickyID)
	}
}

func TestServerAnthropicErrorEnvelope(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return errorResult(model, http.StatusNotFound, `{"error":{"message":"missing","type":"not_found_error"}}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/messages",
		strings.NewReader(`{"model":"model/a","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "key-a")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	if body["type"] != "error" {
		t.Fatalf("envelope = %v", body)
	}
	if body["request_id"] != response.Header.Get("x-request-id") {
		t.Fatalf("request id mismatch: %v vs %q", body["request_id"], response.Header.Get("x-request-id"))
	}
}

func TestServerChatRewritesModelAndSetsHeaders(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
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
	protocol := router.lastProtocol
	operation := router.lastOperation
	router.mu.Unlock()
	if protocol != wireOpenAI || operation != "chat" {
		t.Fatalf("dispatch = %q/%q", protocol, operation)
	}
}

func TestServerUpstreamInvalidJSON(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
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

func TestServerEmbeddingsPassthrough(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	router := &fakeRouter{
		raw: func(model *Model, operation string, payload map[string]any, stickyID string) (*RouteResult, error) {
			captured = payload
			return staticResult(model, http.StatusOK, `{"object":"list","data":[]}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/embeddings", "key-a",
		`{"model":"model/a","input":"hello"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	if body["object"] != "list" {
		t.Fatalf("body = %v", body)
	}
	if captured["input"] != "hello" {
		t.Fatalf("payload = %v", captured)
	}
}

func TestServerStreamInterruptionEmitsError(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			body := &failingStream{
				chunks: []string{
					`data: {"id":"up","model":"m","choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
				},
			}
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    "openai",
				Status:        http.StatusOK,
				Header:        http.Header{},
				Body:          body,
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response.Header.Get("x-accel-buffering") != "no" || response.Header.Get("cache-control") != "no-cache" {
		t.Fatalf("stream headers = %v", response.Header)
	}
	if response.Header.Get("content-type") != "text/event-stream" {
		t.Fatalf("content type = %q", response.Header.Get("content-type"))
	}

	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "hi") {
		t.Fatalf("stream missing content: %s", output)
	}
	if !strings.Contains(output, `"error"`) {
		t.Fatalf("stream missing error event: %s", output)
	}
	if !strings.Contains(output, "[DONE]") {
		t.Fatalf("stream missing DONE marker: %s", output)
	}
	if !strings.HasSuffix(output, "data: [DONE]\n\n") {
		t.Fatalf("stream must end with the error DONE marker: %s", output)
	}
}

func TestServerUpstreamStreamErrorIsTerminal(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    "responses",
				Status:        http.StatusOK,
				Header:        http.Header{},
				Body: io.NopCloser(strings.NewReader(
					`data: {"type":"response.failed","response":{"error":{"message":"boom","type":"server_error"}}}` + "\n\n" +
						`data: {"type":"response.output_text.delta","output_index":0,"delta":"late"}` + "\n\n")),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	data, _ := io.ReadAll(response.Body)
	output := string(data)
	if !strings.Contains(output, "boom") {
		t.Fatalf("stream missing error: %s", output)
	}
	if strings.Contains(output, "late") {
		t.Fatalf("stream must stop at the error event: %s", output)
	}
	if !strings.HasSuffix(output, "data: [DONE]\n\n") {
		t.Fatalf("stream must end with DONE: %s", output)
	}
}

func TestServerAnthropicStreamInterruptionStopsAfterError(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    "openai",
				Status:        http.StatusOK,
				Header:        http.Header{},
				Body: &failingStream{
					chunks: []string{
						`data: {"id":"up","model":"m","choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
					},
				},
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/messages",
		strings.NewReader(`{"model":"model/a","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "key-a")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	data, _ := io.ReadAll(response.Body)
	output := string(data)
	if !strings.Contains(output, "content_block_delta") || !strings.Contains(output, "event: error") {
		t.Fatalf("stream missing content or error event: %s", output)
	}
	if strings.Contains(output, "message_delta") || strings.Contains(output, "message_stop") {
		t.Fatalf("terminal frames must not follow the error event: %s", output)
	}
}

func TestServerStreamConversionAnthropic(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    "openai",
				Status:        http.StatusOK,
				Header:        http.Header{},
				Body:          io.NopCloser(strings.NewReader(openAIChatStream)),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/messages",
		strings.NewReader(`{"model":"model/a","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "key-a")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	data, _ := io.ReadAll(response.Body)
	output := string(data)
	for _, needle := range []string{"message_start", "content_block_delta", "message_stop", "gateway-model", "hi"} {
		if needle == "gateway-model" {
			continue
		}
		if !strings.Contains(output, needle) {
			t.Fatalf("anthropic stream missing %q: %s", needle, output)
		}
	}
	if !strings.Contains(output, "model/a") {
		t.Fatalf("stream must rewrite the model: %s", output)
	}
}

const openAIChatStream = "data: {\"id\":\"up\",\"model\":\"up-model\",\"created\":1," +
	"\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

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
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	router.mu.Lock()
	inbound := router.lastInboundHeads
	stickyID := router.lastStickyID
	protocol := router.lastProtocol
	router.mu.Unlock()

	if protocol != wireAnthropic {
		t.Fatalf("protocol = %q", protocol)
	}
	if inbound["anthropic-version"] != "2024-01-01" || inbound["anthropic-beta"] != "beta-1" {
		t.Fatalf("forwarded headers = %v", inbound)
	}
	if stickyID != "client-42" {
		t.Fatalf("sticky id = %q", stickyID)
	}
}

func TestServerUnknownRoute(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	response := doJSON(t, http.MethodGet, server.URL+"/nope", "", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	if body["detail"] != "Not Found" {
		t.Fatalf("body = %v", body)
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

func TestServerRecoversFromPanic(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			panic("boom")
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}
	errorObject := decodeBody(t, response)["error"].(map[string]any)
	if errorObject["code"] != "internal_error" {
		t.Fatalf("error = %v", errorObject)
	}
}

func TestServerGeneratesRequestID(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	response := doJSON(t, http.MethodGet, server.URL+"/api/health", "", "")
	id := response.Header.Get("x-request-id")
	if !strings.HasPrefix(id, "req_") || len(id) != len("req_")+32 {
		t.Fatalf("generated request id = %q", id)
	}

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/health", nil)
	request.Header.Set("x-request-id", "bad id with spaces")
	response2, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response2.Body.Close()
	if got := response2.Header.Get("x-request-id"); !strings.HasPrefix(got, "req_") {
		t.Fatalf("invalid client id must be replaced: %q", got)
	}
}

// failingStream yields its chunks and then fails, simulating an interrupted
// upstream connection.
type failingStream struct {
	chunks []string
	index  int
}

func (s *failingStream) Read(p []byte) (int, error) {
	if s.index < len(s.chunks) {
		data := s.chunks[s.index]
		s.index++
		return copy(p, data), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func (s *failingStream) Close() error { return nil }

func TestServerStreamsRawBytes(t *testing.T) {
	t.Parallel()

	payload := `{"object":"list","data":[{"embedding":[0.1]}]}`
	router := &fakeRouter{
		raw: func(model *Model, operation string, body map[string]any, stickyID string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    "openai",
				Status:        http.StatusOK,
				Header:        http.Header{},
				Body:          io.NopCloser(bytes.NewReader([]byte(payload))),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/embeddings", "key-a",
		`{"model":"model/a","input":"hi","stream":true}`)
	data, _ := io.ReadAll(response.Body)
	if string(data) != payload {
		t.Fatalf("raw passthrough = %q", data)
	}
}

func TestServerMethodNotAllowedUsesJSONEnvelope(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	response := doJSON(t, http.MethodGet, server.URL+"/api/v1/chat/completions", "", "")
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response.Header.Get("Allow") == "" {
		t.Fatal("missing Allow header")
	}
	body := decodeBody(t, response)
	errorObject, ok := body["error"].(map[string]any)
	if !ok || errorObject["code"] != "method_not_allowed" {
		t.Fatalf("body = %v", body)
	}
}

func TestServerConvertedJSONErrorObject(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return staticResult(model, http.StatusOK,
				`{"error":{"message":"boom","type":"server_error"}}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	// A cross-protocol request forces the IR conversion path, which must not
	// mistake a 200 response that embeds an error for a success.
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/messages",
		strings.NewReader(`{"model":"model/a","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "key-a")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", response.StatusCode)
	}
	data, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(data), "boom") {
		t.Fatalf("error body = %s", data)
	}
}

func TestRewriteStreamModelPreservesKeyOrder(t *testing.T) {
	t.Parallel()

	line := []byte(`data: {"id":"x","system_fingerprint":"fp","model":"up","z":1}` + "\n")
	rewritten, ok := rewriteStreamModel(line, "public")
	if !ok {
		t.Fatal("expected rewrite")
	}
	got := strings.TrimSpace(string(rewritten))
	want := `data: {"id":"x","system_fingerprint":"fp","model":"public","z":1}`
	if got != want {
		t.Fatalf("key order changed:\n got %s\nwant %s", got, want)
	}

	unchanged, ok := rewriteStreamModel([]byte("data: {\"delta\":\"hi\"}\n"), "public")
	if ok || unchanged != nil {
		t.Fatalf("line without model was rewritten: %s", unchanged)
	}
}

func TestRelayPlan(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		chatFamily   bool
		stream       bool
		sameWire     bool
		upstreamJSON bool
		want         relayMode
	}{
		{"raw", false, false, true, false, relayRaw},
		{"raw stream", false, true, false, false, relayRaw},
		{"passthrough json", true, false, true, false, relayPassthroughJSON},
		{"convert json", true, false, false, false, relayConvertJSON},
		{"passthrough sse", true, true, true, false, relayPassthroughSSE},
		{"convert sse", true, true, false, false, relayConvertSSE},
		{"buffered sse", true, true, false, true, relayBufferedSSE},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := planMode(test.chatFamily, test.stream, test.sameWire, test.upstreamJSON); got != test.want {
				t.Fatalf("planMode = %v, want %v", got, test.want)
			}
		})
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Requests failing before the relay (authentication, body parsing, model
// resolution) must still produce an access-log line with status and duration.
func TestServerLogsFailedRequests(t *testing.T) {
	t.Parallel()

	logs := &syncBuffer{}
	cfg := &Config{UsersByKey: map[string]*User{
		"key-a": {ID: "alice", APIKey: "key-a"},
	}}
	server := NewServer(cfg, &fakeRouter{}, slog.New(slog.NewTextHandler(logs, nil)))
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	response := doJSON(t, http.MethodPost, httpServer.URL+"/api/v1/chat/completions", "",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, response.Body)

	deadline := time.Now().Add(time.Second)
	for {
		output := logs.String()
		if strings.Contains(output, "request completed") && strings.Contains(output, "status=401") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("access log = %q", output)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Method-not-allowed and not-found responses never reach a handler that opts
// into logging, so they must be enabled explicitly.
func TestServerLogsRejectedPaths(t *testing.T) {
	t.Parallel()

	logs := &syncBuffer{}
	server := NewServer(&Config{}, &fakeRouter{}, slog.New(slog.NewTextHandler(logs, nil)))
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	cases := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/v1/chat/completions", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/missing", http.StatusNotFound},
	}
	for _, test := range cases {
		response := doJSON(t, test.method, httpServer.URL+test.path, "", "")
		if response.StatusCode != test.status {
			t.Fatalf("%s %s: status = %d, want %d", test.method, test.path, response.StatusCode, test.status)
		}
		_, _ = io.Copy(io.Discard, response.Body)

		deadline := time.Now().Add(time.Second)
		statusNeedle := "status=" + strconv.Itoa(test.status)
		for {
			if strings.Contains(logs.String(), statusNeedle) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s %s: access log = %q", test.method, test.path, logs.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
