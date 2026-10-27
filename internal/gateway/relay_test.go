package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestServerStreamInterruptionEmitsError(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			body := &failingStream{
				chunks: []string{
					`data: {"id":"up","model":"m","choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
				},
			}
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
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

func TestServerAnthropicStreamInterruptionStopsAfterError(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireMessages,
				Status:        http.StatusOK,
				Header:        http.Header{},
				Body: &failingStream{
					chunks: []string{
						`event: content_block_delta` + "\n" +
							`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n",
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

func TestServerStreamSameWireRewritesModel(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireMessages,
				Status:        http.StatusOK,
				Header:        http.Header{},
				Body:          io.NopCloser(strings.NewReader(anthropicStream)),
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
	for _, needle := range []string{"message_start", "content_block_delta", "message_stop", "hi"} {
		if !strings.Contains(output, needle) {
			t.Fatalf("anthropic stream missing %q: %s", needle, output)
		}
	}
	if !strings.Contains(output, "model/a") {
		t.Fatalf("stream must rewrite the model: %s", output)
	}
}

const anthropicStream = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"up-model\",\"content\":[]}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

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

// A tool argument or message body may contain the literal text `"model": "x"`.
// Only object keys named "model" are rewritten, never string content.
func TestRewriteStreamModelIgnoresModelTextInsideStrings(t *testing.T) {
	t.Parallel()

	line := []byte("data: {\"model\":\"up\",\"choices\":[{\"delta\":{\"content\":\"the \\\"model\\\": \\\"inner\\\" config\"}}]}\n")
	rewritten, ok := rewriteStreamModel(line, "public")
	if !ok {
		t.Fatal("expected rewrite")
	}
	output := string(rewritten)
	if !strings.Contains(output, `"model":"public"`) {
		t.Fatalf("top-level model was not rewritten: %s", output)
	}
	if !strings.Contains(output, `\"model\": \"inner\"`) {
		t.Fatalf("string content was corrupted: %s", output)
	}
}

// A same-protocol stream must keep its exact framing: only the model value is
// replaced, including the field spacing and the line terminator.
func TestRewriteStreamModelPreservesFraming(t *testing.T) {
	t.Parallel()

	line := []byte("data:   {\"model\":\"up\",\"x\":1}\r\n")
	rewritten, ok := rewriteStreamModel(line, "public")
	if !ok {
		t.Fatal("expected rewrite")
	}
	if got, want := string(rewritten), "data:   {\"model\":\"public\",\"x\":1}\r\n"; got != want {
		t.Fatalf("rewritten line = %q, want %q", got, want)
	}

	if out, ok := rewriteStreamModel([]byte("data: [DONE]\n"), "public"); ok || out != nil {
		t.Fatalf("[DONE] sentinel was rewritten: %s", out)
	}
}

func TestRelayPlan(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		sameWire     bool
		upstreamJSON bool
		want         relayMode
	}{
		{"json passthrough", true, true, relayPassthroughJSON},
		{"json conversion", false, true, relayConvertJSON},
		{"sse passthrough", true, false, relayPassthroughSSE},
		{"sse conversion", false, false, relayConvertSSE},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := planMode(test.sameWire, test.upstreamJSON); got != test.want {
				t.Fatalf("planMode = %v, want %v", got, test.want)
			}
		})
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
