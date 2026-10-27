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
