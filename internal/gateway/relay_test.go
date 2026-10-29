package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/core"
	"github.com/ratsdev/llm-proxy/internal/router"
	"github.com/ratsdev/llm-proxy/internal/util"
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

// Error events on interrupted and corrupt upstream streams.

// A converted stream must not end silently when the upstream sends a chunk that
// is not JSON: the caller receives the inbound protocol's terminal error event
// instead of a truncated reply that looks complete.
func TestConvertedStreamEmitsErrorEventOnCorruptChunk(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
				Status:        http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"id\":\"chatcmpl-1\",\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
						"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" +
						"data: {not json\n\n")),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/messages", "key-a",
		`{"model":"model/a","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	body := readBody(t, response)

	if !strings.Contains(body, "event: error") {
		t.Fatalf("stream did not end with a terminal error event:\n%s", body)
	}
	if !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("terminal error event has no error envelope:\n%s", body)
	}
	if strings.Contains(body, "event: message_stop") {
		t.Fatalf("stream reported a clean stop after a corrupt chunk:\n%s", body)
	}
}

// A passthrough stream keeps its bytes, but an interrupted upstream still has to
// terminate with the caller's error event.
func TestPassthroughStreamEmitsErrorEventOnReadFailure(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
				Status:        http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(io.MultiReader(
					strings.NewReader("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"),
					errReader{err: io.ErrUnexpectedEOF},
				)),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	body := readBody(t, response)

	if !strings.Contains(body, `"content":"hi"`) {
		t.Fatalf("relayed bytes were lost:\n%s", body)
	}
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "unexpected EOF") {
		t.Fatalf("interrupted stream did not report the failure:\n%s", body)
	}
}

// errReader fails every read after the reader in front of it is exhausted.
type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}

// Usage accounting, from buffered bodies and from stream lines.

func TestUsageFromObject(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		body   string
		want   [3]int // prompt, output, total
		wantOK bool
	}{
		{
			name:   "openai chat",
			body:   `{"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`,
			want:   [3]int{10, 4, 14},
			wantOK: true,
		},
		{
			name:   "openai responses with cached tokens",
			body:   `{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":3}}}`,
			want:   [3]int{10, 4, 14},
			wantOK: true,
		},
		{
			name:   "anthropic nested message with cache",
			body:   `{"message":{"usage":{"input_tokens":5,"output_tokens":6,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`,
			want:   [3]int{8, 6, 14},
			wantOK: true,
		},
		{
			name:   "responses event nested response",
			body:   `{"type":"response.completed","response":{"usage":{"input_tokens":7,"output_tokens":3}}}`,
			want:   [3]int{7, 3, 10},
			wantOK: true,
		},
		{
			name:   "no usage",
			body:   `{"id":"x"}`,
			wantOK: false,
		},
	}

	for _, testCase := range cases {
		value, err := util.Decode([]byte(testCase.body))
		if err != nil {
			t.Fatalf("%s: decode: %v", testCase.name, err)
		}
		object, ok := util.AsMap(value)
		if !ok {
			t.Fatalf("%s: not an object", testCase.name)
		}
		usage, ok := usageFromObject(object)
		if ok != testCase.wantOK {
			t.Fatalf("%s: ok = %v", testCase.name, ok)
		}
		if !ok {
			continue
		}
		got := [3]int{usage.PromptTokens(), usage.OutputTokens, usage.TotalTokens()}
		if got != testCase.want {
			t.Fatalf("%s: usage = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestUsageFromStreamLine(t *testing.T) {
	t.Parallel()

	usage, ok := usageFromStreamLine([]byte(`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	if !ok || usage.PromptTokens() != 3 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, ok = %v", usage, ok)
	}

	for _, line := range []string{
		`event: message`,
		`data: [DONE]`,
		`data: {"id":"x"}`,
		`not sse`,
	} {
		if _, ok := usageFromStreamLine([]byte(line)); ok {
			t.Fatalf("line %q unexpectedly produced usage", line)
		}
	}
}

// Cross-format conversion on the wire.

// conversionPairs lists every cross-format direction the endpoint converter
// registry advertises. Same-format traffic is covered by the passthrough tests.
var conversionPairs = []struct {
	inbound  WireFormat
	upstream WireFormat
}{
	{wireChatCompletions, wireMessages},
	{wireChatCompletions, wireResponses},
	{wireMessages, wireChatCompletions},
	{wireMessages, wireResponses},
	{wireResponses, wireChatCompletions},
	{wireResponses, wireMessages},
}

func conversionRequest(wire WireFormat, stream bool) string {
	switch wire {
	case wireMessages:
		if stream {
			return `{"model":"model/a","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		}
		return `{"model":"model/a","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
	case wireResponses:
		if stream {
			return `{"model":"model/a","stream":true,"input":"hi"}`
		}
		return `{"model":"model/a","input":"hi"}`
	default:
		if stream {
			return `{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`
		}
		return `{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`
	}
}

func conversionRoute(wire WireFormat) string {
	spec, _ := core.RouteSpecForWire(wire)
	return spec.Inbound
}

func conversionResponse() *ir.Response {
	stop := ir.EndTurn
	return &ir.Response{
		ID:         "resp_1",
		Model:      "model-a",
		Content:    []ir.Block{{Type: ir.Text, Text: "hello"}},
		StopReason: &stop,
		Usage:      ir.Usage{InputTokens: 3, OutputTokens: 2},
	}
}

func upstreamJSON(t *testing.T, wire WireFormat) string {
	t.Helper()
	payload := convert.AdapterFor(wire).FormatResponse(conversionResponse(), "model-a")
	encoded, err := util.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s response: %v", wire, err)
	}
	return string(encoded)
}

func upstreamSSE(t *testing.T, wire WireFormat) string {
	t.Helper()
	response := conversionResponse()
	events := []ir.StreamEvent{
		{Type: ir.MessageStart, MessageID: response.ID, Model: response.Model},
		{Type: ir.ContentBlockStart, Index: 0, Block: &ir.Block{Type: ir.Text}},
		{Type: ir.ContentBlockDelta, Index: 0, DeltaType: ir.DeltaText, Delta: response.Content[0].Text},
		{Type: ir.ContentBlockStop, Index: 0},
		{Type: ir.MessageDelta, StopReason: response.StopReason, Usage: &response.Usage},
		{Type: ir.MessageStop},
	}

	encoder := convert.AdapterFor(wire).NewEncoder("model-a", true)
	var body bytes.Buffer
	for _, event := range events {
		frames, err := encoder.Encode(event)
		if err != nil {
			t.Fatalf("encode %s event: %v", wire, err)
		}
		for _, frame := range frames {
			body.Write(frame)
		}
	}
	for _, frame := range encoder.Finish() {
		body.Write(frame)
	}
	return body.String()
}

// TestServerConvertsNonStreamResponses exercises the response side of every
// cross-format converter: the upstream reply is written in its own wire format
// and the client receives its own.
func TestServerConvertsNonStreamResponses(t *testing.T) {
	t.Parallel()

	for _, pair := range conversionPairs {
		pair := pair
		t.Run(string(pair.inbound)+"_from_"+string(pair.upstream), func(t *testing.T) {
			t.Parallel()

			body := upstreamJSON(t, pair.upstream)
			router := &fakeRouter{
				send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
					return &RouteResult{
						ProviderID:    "provider",
						UpstreamModel: "model-a",
						WireFormat:    pair.upstream,
						Status:        http.StatusOK,
						Header:        http.Header{"Content-Type": []string{"application/json"}},
						Body:          io.NopCloser(strings.NewReader(body)),
					}, nil
				},
			}
			server := newTestServer(t, router, nil)

			response := doJSON(t, http.MethodPost, server.URL+conversionRoute(pair.inbound), "key-a",
				conversionRequest(pair.inbound, false))
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			decoded := decodeBody(t, response)
			parsed, err := convert.AdapterFor(pair.inbound).ParseResponse(decoded)
			if err != nil {
				t.Fatalf("parse converted response: %v (%v)", err, decoded)
			}
			if decoded["model"] != "model/a" {
				t.Fatalf("model = %v", decoded["model"])
			}
			if !strings.Contains(responseText(parsed), "hello") {
				t.Fatalf("converted content = %+v", parsed.Content)
			}
			if parsed.Usage.InputTokens != 3 || parsed.Usage.OutputTokens != 2 {
				t.Fatalf("usage = %+v", parsed.Usage)
			}
		})
	}
}

// TestServerConvertsStreamResponses decodes every upstream SSE dialect and
// re-encodes it for the caller's dialect.
func TestServerConvertsStreamResponses(t *testing.T) {
	t.Parallel()

	for _, pair := range conversionPairs {
		pair := pair
		t.Run(string(pair.inbound)+"_from_"+string(pair.upstream), func(t *testing.T) {
			t.Parallel()

			body := upstreamSSE(t, pair.upstream)
			router := &fakeRouter{
				send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
					return &RouteResult{
						ProviderID:    "provider",
						UpstreamModel: "model-a",
						WireFormat:    pair.upstream,
						Status:        http.StatusOK,
						Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:          io.NopCloser(strings.NewReader(body)),
					}, nil
				},
			}
			server := newTestServer(t, router, nil)

			response := doJSON(t, http.MethodPost, server.URL+conversionRoute(pair.inbound), "key-a",
				conversionRequest(pair.inbound, true))
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			data, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read stream: %v", err)
			}

			decoder := convert.AdapterFor(pair.inbound).NewDecoder()
			var text strings.Builder
			if err := util.Parse(bytes.NewReader(data), util.DefaultMaxSSELineBytes, func(event, payload string) error {
				events, err := decoder.Feed(event, payload)
				if err != nil {
					return err
				}
				for _, streamEvent := range events {
					if streamEvent.Type == ir.ContentBlockDelta && streamEvent.DeltaType == ir.DeltaText {
						text.WriteString(streamEvent.Delta)
					}
					if streamEvent.Type == ir.ContentBlockStart && streamEvent.Block != nil && streamEvent.Block.Type == ir.Text {
						text.WriteString(streamEvent.Block.Text)
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("parse converted stream: %v\n%s", err, data)
			}
			if strings.TrimSpace(text.String()) != "hello" {
				t.Fatalf("converted text = %q\n%s", text.String(), data)
			}
		})
	}
}

// TestRouterConvertsRequestAndResponse wires a chat_completions inbound endpoint
// to a Messages upstream through the converter registry.
func TestRouterConvertsRequestAndResponse(t *testing.T) {
	t.Parallel()

	var upstreamPayload map[string]any
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		value, err := util.Decode(readBodyAll(t, r.Body))
		if err != nil {
			t.Errorf("decode upstream body: %v", err)
			return
		}
		upstreamPayload, _ = util.AsMap(value)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamJSON(t, wireMessages)))
	})

	provider := testProvider("p", server.URL, "/chat/completions")
	provider.Endpoints[wireChatCompletions] = ProviderEndpoint{
		Path:      "/v1/messages",
		Converter: convert.OpenAIChatToClaudeMessages,
	}
	model := &Model{ID: "model/a", Providers: []ModelProvider{{ProviderID: "p", UpstreamModel: "claude-up"}}}
	routing, err := router.NewRouter(testConfig([]*Provider{provider}, model))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer routing.Close()

	server2 := newTestServer(t, routing, func(cfg *Config) {
		cfg.Providers = map[string]*Provider{"p": provider}
		cfg.Models = map[string]*Model{"model/a": model}
	})
	response := doJSON(t, http.MethodPost, server2.URL+conversionRoute(wireChatCompletions), "key-a",
		conversionRequest(wireChatCompletions, false))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	if upstreamPayload["model"] != "claude-up" {
		t.Fatalf("upstream model = %v", upstreamPayload["model"])
	}
	if upstreamPayload["system"] != nil {
		t.Fatalf("unexpected system: %v", upstreamPayload["system"])
	}
	if _, ok := upstreamPayload["messages"].([]any); !ok {
		t.Fatalf("upstream messages = %v", upstreamPayload["messages"])
	}
	if _, ok := upstreamPayload["max_tokens"]; !ok {
		t.Fatalf("upstream max_tokens missing: %v", upstreamPayload)
	}

	decoded := decodeBody(t, response)
	choices, _ := util.AsSlice(decoded["choices"])
	if len(choices) == 0 {
		t.Fatalf("converted response has no choices: %v", decoded)
	}
	choice, _ := util.AsMap(choices[0])
	message, _ := util.AsMap(choice["message"])
	if message["content"] != "hello" {
		t.Fatalf("converted message = %v", message)
	}
}

func responseText(response *ir.Response) string {
	var text strings.Builder
	for _, block := range response.Content {
		text.WriteString(block.Text)
	}
	return text.String()
}

func readBodyAll(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return data
}
