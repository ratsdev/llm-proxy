// Package protocols_test exercises the wire-format adapters two ways: a full
// inbound/upstream matrix that round-trips a rich request, response and stream
// through all six conversion directions, and compatibility regressions derived
// from the behaviour of other LLM gateways (litellm, bifrost) and the
// OpenAI/Anthropic wire specifications. Each compatibility test states the
// behaviour a client is entitled to expect; failures identify functional
// defects rather than stylistic differences.
package protocols_test

import (
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols/anthropic"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols/openai"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// Adapter matrix.

var wireFormats = []string{"openai", "anthropic", "responses"}

func adapters() map[string]protocols.Adapter {
	return map[string]protocols.Adapter{
		"openai":    openai.Chat{},
		"anthropic": anthropic.Messages{},
		"responses": openai.Responses{},
	}
}

func object(t *testing.T, raw string) map[string]any {
	t.Helper()
	value, err := util.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	result, ok := util.AsMap(value)
	if !ok {
		t.Fatalf("expected object, got %T", value)
	}
	return result
}

func marshal(t *testing.T, value any) string {
	t.Helper()
	encoded, err := util.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

func inboundRequest(t *testing.T, protocol string) map[string]any {
	t.Helper()
	switch protocol {
	case "openai":
		return object(t, `{
			"model": "public-model",
			"messages": [
				{"role": "system", "content": "sys"},
				{"role": "user", "content": [
					{"type": "text", "text": "hello"},
					{"type": "image_url", "image_url": {"url": "https://img"}}
				]},
				{"role": "assistant", "content": "calling", "tool_calls": [{"id": "c1", "function": {"name": "f", "arguments": "{}"}}]},
				{"role": "tool", "tool_call_id": "c1", "content": "result"}
			],
			"max_tokens": 64,
			"tools": [{"type": "function", "function": {"name": "f", "parameters": {"type": "object"}}}]
		}`)
	case "anthropic":
		return object(t, `{
			"model": "public-model",
			"system": "sys",
			"messages": [
				{"role": "user", "content": [
					{"type": "text", "text": "hello"},
					{"type": "image", "source": {"type": "url", "url": "https://img"}}
				]},
				{"role": "assistant", "content": [
					{"type": "text", "text": "calling"},
					{"type": "tool_use", "id": "c1", "name": "f", "input": {}}
				]},
				{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "c1", "content": "result"}]}
			],
			"max_tokens": 64,
			"tools": [{"name": "f", "input_schema": {"type": "object"}}]
		}`)
	default:
		return object(t, `{
			"model": "public-model",
			"instructions": "sys",
			"input": [
				{"type": "message", "role": "user", "content": [
					{"type": "input_text", "text": "hello"},
					{"type": "input_image", "image_url": "https://img"}
				]},
				{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "calling"}]},
				{"type": "function_call", "call_id": "c1", "name": "f", "arguments": "{}"},
				{"type": "function_call_output", "call_id": "c1", "output": "result"}
			],
			"max_output_tokens": 64,
			"tools": [{"type": "function", "name": "f", "parameters": {"type": "object"}}]
		}`)
	}
}

func TestRequestMatrix(t *testing.T) {
	t.Parallel()

	registry := adapters()
	for _, inbound := range wireFormats {
		inbound := inbound
		t.Run(inbound, func(t *testing.T) {
			t.Parallel()

			request, err := registry[inbound].ParseRequest(inboundRequest(t, inbound))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(request.System) == 0 || ir.JoinText(request.System) != "sys" {
				t.Fatalf("system = %+v", request.System)
			}
			if len(request.Tools) != 1 || request.Tools[0].Name != "f" {
				t.Fatalf("tools = %+v", request.Tools)
			}
			if request.MaxTokens == nil || *request.MaxTokens != 64 {
				t.Fatalf("max tokens = %v", request.MaxTokens)
			}

			var text, image bool
			var toolUse, toolResult bool
			for _, message := range request.Messages {
				for _, block := range message.Content {
					switch block.Type {
					case ir.Text:
						if block.Text == "hello" {
							text = true
						}
					case ir.Image:
						image = true
					case ir.ToolUse:
						toolUse = true
					case ir.ToolResult:
						toolResult = true
					}
				}
			}
			if !text || !image || !toolUse || !toolResult {
				t.Fatalf("incomplete IR: text=%v image=%v toolUse=%v toolResult=%v", text, image, toolUse, toolResult)
			}

			for _, upstream := range wireFormats {
				upstream := upstream
				t.Run("to_"+upstream, func(t *testing.T) {
					t.Parallel()

					body, err := registry[upstream].FormatRequest(request, "upstream-model", intPtr(256))
					if err != nil {
						t.Fatalf("format: %v", err)
					}
					if body["model"] != "upstream-model" {
						t.Fatalf("model = %v", body["model"])
					}
					encoded := marshal(t, body)
					for _, needle := range []string{"hello", "https://img", "sys", "c1", "result", "f"} {
						if !strings.Contains(encoded, needle) {
							t.Fatalf("%s output missing %q: %s", upstream, needle, encoded)
						}
					}

					switch upstream {
					case "openai":
						messages, ok := util.AsSlice(body["messages"])
						if !ok || len(messages) == 0 {
							t.Fatalf("messages = %v", body["messages"])
						}
					case "anthropic":
						messages, ok := util.AsSlice(body["messages"])
						if !ok || len(messages) == 0 {
							t.Fatalf("messages = %v", body["messages"])
						}
						if body["system"] != "sys" {
							t.Fatalf("system = %v", body["system"])
						}
					case "responses":
						items, ok := util.AsSlice(body["input"])
						if !ok || len(items) == 0 {
							t.Fatalf("input = %v", body["input"])
						}
						if body["instructions"] != "sys" {
							t.Fatalf("instructions = %v", body["instructions"])
						}
					}
				})
			}
		})
	}
}

func canonicalResponse(stopReason string, usage ir.Usage) *ir.Response {
	reason := stopReason
	content := []ir.Block{
		{Type: ir.Thinking, Text: "think"},
		ir.TextBlock("hello"),
	}
	if stopReason == ir.ToolUseStop {
		content = append(content, ir.Block{Type: ir.ToolUse, ToolUseID: "c1", ToolName: "f", ToolInput: "{}"})
	}
	return &ir.Response{
		ID:         "upstream-id",
		Model:      "upstream-model",
		Content:    content,
		StopReason: &reason,
		Usage:      usage,
		Created:    123,
	}
}

func TestResponseMatrix(t *testing.T) {
	t.Parallel()

	registry := adapters()
	for _, inbound := range wireFormats {
		inbound := inbound
		t.Run(inbound, func(t *testing.T) {
			t.Parallel()

			for _, stopReason := range []string{ir.EndTurn, ir.MaxTokens, ir.Refusal, ir.ToolUseStop} {
				stopReason := stopReason
				t.Run(stopReason, func(t *testing.T) {
					t.Parallel()

					response := canonicalResponse(stopReason, ir.Usage{
						InputTokens:          5,
						OutputTokens:         3,
						CacheReadInputTokens: 2,
					})
					payload := registry[inbound].FormatResponse(response, "gateway-model")
					encoded := marshal(t, payload)
					needles := []string{"hello", "gateway-model"}
					if stopReason == ir.ToolUseStop {
						needles = append(needles, "c1")
					}
					for _, needle := range needles {
						if !strings.Contains(encoded, needle) {
							t.Fatalf("%s response missing %q: %s", inbound, needle, encoded)
						}
					}

					parsed, err := registry[inbound].ParseResponse(payload)
					if err != nil {
						t.Fatalf("parse: %v", err)
					}
					if parsed.StopReason == nil || *parsed.StopReason != stopReason {
						t.Fatalf("stop reason = %v, want %q", parsed.StopReason, stopReason)
					}

					var text, tool, thinking bool
					for _, block := range parsed.Content {
						switch block.Type {
						case ir.Text:
							text = text || block.Text == "hello"
						case ir.ToolUse:
							tool = tool || block.ToolUseID == "c1"
						case ir.Thinking:
							thinking = thinking || block.Text == "think"
						}
					}
					if !text || !thinking || (stopReason == ir.ToolUseStop && !tool) {
						t.Fatalf("round trip lost blocks: %+v", parsed.Content)
					}
				})
			}
		})
	}
}

func upstreamChunks(protocol string) []string {
	switch protocol {
	case "openai":
		return []string{
			`{"id":"up-id","model":"up","created":1,"choices":[{"delta":{"content":"hello"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		}
	case "anthropic":
		return []string{
			`{"type":"message_start","message":{"id":"up-id","model":"up","usage":{"input_tokens":3}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c1","name":"f"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		}
	default:
		return []string{
			`{"type":"response.created","response":{"id":"up-id","model":"up","created_at":1}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message"}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"c1","name":"f"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{}"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"id":"up-id","model":"up","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":4}}}`,
		}
	}
}

func TestStreamMatrix(t *testing.T) {
	t.Parallel()

	registry := adapters()
	for _, upstream := range wireFormats {
		upstream := upstream
		t.Run(upstream, func(t *testing.T) {
			t.Parallel()

			decoder := registry[upstream].NewDecoder()
			var events []ir.StreamEvent
			for _, chunk := range upstreamChunks(upstream) {
				produced, err := decoder.Feed("", chunk)
				if err != nil {
					t.Fatalf("feed: %v", err)
				}
				events = append(events, produced...)
			}
			events = append(events, decoder.Finish()...)
			if len(events) == 0 {
				t.Fatal("decoder produced no events")
			}

			for _, inbound := range wireFormats {
				inbound := inbound
				t.Run("to_"+inbound, func(t *testing.T) {
					t.Parallel()

					encoder := registry[inbound].NewEncoder("gateway-model", true)
					var frames [][]byte
					for _, event := range events {
						produced, err := encoder.Encode(event)
						if err != nil {
							t.Fatalf("encode: %v", err)
						}
						frames = append(frames, produced...)
					}
					frames = append(frames, encoder.Finish()...)

					var builder strings.Builder
					for _, frame := range frames {
						builder.Write(frame)
					}
					output := builder.String()
					if !strings.Contains(output, "hello") {
						t.Fatalf("%s output missing text: %s", inbound, output)
					}
					if !strings.Contains(output, "gateway-model") {
						t.Fatalf("%s output missing model: %s", inbound, output)
					}

					terminal := map[string]string{
						"openai":    "[DONE]",
						"anthropic": "message_stop",
						"responses": "response.completed",
					}[inbound]
					if !strings.Contains(output, terminal) {
						t.Fatalf("%s output missing terminal %q: %s", inbound, terminal, output)
					}
				})
			}
		})
	}
}

func TestProtocolErrorEnvelopes(t *testing.T) {
	t.Parallel()

	registry := adapters()
	openAI := registry["openai"].FormatError(429, "slow", "rate_limit_error", "rate", "req")
	if _, ok := openAI["error"]; !ok {
		t.Fatalf("openai envelope = %v", openAI)
	}
	responses := registry["responses"].FormatError(429, "slow", "rate_limit_error", "rate", "req")
	if marshal(t, responses) != marshal(t, openAI) {
		t.Fatalf("responses must reuse the OpenAI envelope: %s vs %s", marshal(t, responses), marshal(t, openAI))
	}
	anthropicPayload := registry["anthropic"].FormatError(429, "slow", "", "", "req")
	if anthropicPayload["type"] != "error" || anthropicPayload["request_id"] != "req" {
		t.Fatalf("anthropic envelope = %v", anthropicPayload)
	}
}

func intPtr(value int) *int { return &value }

// Compatibility regressions.

// Some OpenAI-compatible upstreams stream tool calls without the "index"
// field. Distinct calls must stay distinct; they must not be folded into one
// block whose arguments become invalid JSON.
func TestCompatChatStreamKeepsDistinctToolCallsWithoutIndex(t *testing.T) {
	t.Parallel()

	decoder := openai.Chat{}.NewDecoder()
	chunks := []string{
		`{"id":"x","model":"u","choices":[{"delta":{"tool_calls":[{"id":"c1","function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`,
		`{"id":"x","model":"u","choices":[{"delta":{"tool_calls":[{"id":"c2","function":{"name":"g","arguments":"{\"b\":2}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	var blocks []ir.Block
	var arguments []string
	for _, chunk := range chunks {
		events, err := decoder.Feed("", chunk)
		if err != nil {
			t.Fatalf("feed: %v", err)
		}
		for _, event := range events {
			switch {
			case event.Type == ir.ContentBlockStart && event.Block != nil && event.Block.Type == ir.ToolUse:
				blocks = append(blocks, *event.Block)
			case event.Type == ir.ContentBlockDelta && event.DeltaType == ir.DeltaInputJSON:
				arguments = append(arguments, event.Delta)
			}
		}
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 tool_use blocks, got %d (arguments merged: %v)", len(blocks), arguments)
	}
	if blocks[0].ToolUseID != "c1" || blocks[0].ToolName != "f" {
		t.Fatalf("first tool block = %+v", blocks[0])
	}
	if blocks[1].ToolUseID != "c2" || blocks[1].ToolName != "g" {
		t.Fatalf("second tool block = %+v", blocks[1])
	}
}

// Chat Completions "response_format" and Responses "text.format" express the
// same structured-output contract; a gateway that converts between the two
// APIs must translate it instead of dropping it.
func TestCompatChatToResponsesKeepsStructuredOutput(t *testing.T) {
	t.Parallel()

	request, err := openai.Chat{}.ParseRequest(object(t, `{
		"model":"m",
		"messages":[{"role":"user","content":"hi"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{"name":"out","schema":{"type":"object","properties":{"a":{"type":"string"}}}}
		}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	body, err := openai.Responses{}.FormatRequest(request, "up", nil)
	if err != nil {
		t.Fatalf("format: %v", err)
	}

	text, ok := util.AsMap(body["text"])
	if !ok {
		t.Fatalf("structured output dropped: %v", body)
	}
	format, ok := util.AsMap(text["format"])
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("text.format = %v", text)
	}
	if format["name"] != "out" {
		t.Fatalf("text.format.name = %v", format["name"])
	}
	if _, ok := util.AsMap(format["schema"]); !ok {
		t.Fatalf("text.format.schema = %v", format["schema"])
	}
}

func TestCompatResponsesToChatKeepsStructuredOutput(t *testing.T) {
	t.Parallel()

	request, err := openai.Responses{}.ParseRequest(object(t, `{
		"model":"m",
		"input":"hi",
		"text":{"format":{
			"type":"json_schema",
			"name":"out",
			"schema":{"type":"object","properties":{"a":{"type":"string"}}}
		}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	body, err := openai.Chat{}.FormatRequest(request, "up", nil)
	if err != nil {
		t.Fatalf("format: %v", err)
	}

	responseFormat, ok := util.AsMap(body["response_format"])
	if !ok {
		t.Fatalf("structured output dropped: %v", body)
	}
	if responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format = %v", responseFormat)
	}
	schema, ok := util.AsMap(responseFormat["json_schema"])
	if !ok || schema["name"] != "out" {
		t.Fatalf("response_format.json_schema = %v", responseFormat["json_schema"])
	}
}

// cache_control drives Anthropic prompt caching. Converting Anthropic ->
// Anthropic must not strip it, otherwise every request re-pays for the
// cached prefix.
func TestCompatAnthropicKeepsCacheControl(t *testing.T) {
	t.Parallel()

	body := object(t, `{
		"model":"m","max_tokens":64,
		"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}
		]
	}`)
	request, err := anthropic.Messages{}.ParseRequest(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	formatted, err := anthropic.Messages{}.FormatRequest(request, "up", nil)
	if err != nil {
		t.Fatalf("format: %v", err)
	}

	encoded := marshal(t, formatted)
	if !strings.Contains(encoded, "cache_control") {
		t.Fatalf("cache_control dropped: %s", encoded)
	}
}

// Chat Completions renamed max_tokens to max_completion_tokens for reasoning
// models (o1/o3/gpt-5). A same-protocol proxy must forward the key the client
// used; sending max_tokens to those models is rejected by OpenAI.
func TestCompatChatKeepsMaxCompletionTokens(t *testing.T) {
	t.Parallel()

	request, err := openai.Chat{}.ParseRequest(object(t, `{
		"model":"m",
		"messages":[{"role":"user","content":"hi"}],
		"max_completion_tokens":123
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	body, err := openai.Chat{}.FormatRequest(request, "up", nil)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if body["max_completion_tokens"] != 123 {
		t.Fatalf("max_completion_tokens = %v (max_tokens = %v)", body["max_completion_tokens"], body["max_tokens"])
	}
}

// "n" is a same-protocol Chat field. Cross-protocol Extra no longer carries
// it; the HTTP passthrough path forwards the original JSON instead.
func TestCompatChatOmitsChoiceCountFromExtra(t *testing.T) {
	t.Parallel()

	request, err := openai.Chat{}.ParseRequest(object(t, `{
		"model":"m",
		"messages":[{"role":"user","content":"hi"}],
		"n":2
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := request.Extra["n"]; ok {
		t.Fatalf("n should not be in Extra: %+v", request.Extra)
	}
}

// Responses streams may deliver tool arguments only in the authoritative
// "response.function_call_arguments.done" event. Skipping it yields `{}`
// arguments downstream.
func TestCompatResponsesStreamKeepsDoneOnlyToolArguments(t *testing.T) {
	t.Parallel()

	decoder := openai.Responses{}.NewDecoder()
	chunks := []string{
		`{"type":"response.created","response":{"id":"r","model":"u","created_at":1}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c1","name":"f","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","arguments":"{\"city\":\"Paris\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"c1","name":"f","arguments":"{\"city\":\"Paris\"}"}}`,
		`{"type":"response.completed","response":{"id":"r","model":"u","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}
	var events []ir.StreamEvent
	for _, chunk := range chunks {
		produced, err := decoder.Feed("", chunk)
		if err != nil {
			t.Fatalf("feed: %v", err)
		}
		events = append(events, produced...)
	}
	events = append(events, decoder.Finish()...)

	encoder := openai.Responses{}.NewEncoder("model", true)
	var output strings.Builder
	for _, event := range events {
		frames, err := encoder.Encode(event)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		for _, frame := range frames {
			output.Write(frame)
		}
	}
	for _, frame := range encoder.Finish() {
		output.Write(frame)
	}
	if !strings.Contains(output.String(), "Paris") {
		t.Fatalf("tool arguments lost: %s", output.String())
	}
}
