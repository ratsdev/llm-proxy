package protocols_test

// Compatibility regression tests derived from the behaviour of other LLM
// gateways (litellm, bifrost) and the OpenAI/Anthropic wire specifications.
// Each test states the behaviour a client is entitled to expect; failures
// identify functional defects rather than stylistic differences.

import (
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/protocols/anthropic"
	"github.com/ratsdev/llm-proxy/protocols/openai"
	"github.com/ratsdev/llm-proxy/util"
)

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
