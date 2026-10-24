package openai

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/protocols"
	"github.com/ratsdev/llm-proxy/util"
)

func decodeJSON(t *testing.T, data string) map[string]any {
	t.Helper()
	value, err := util.Decode([]byte(data))
	if err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	object, ok := util.AsMap(value)
	if !ok {
		t.Fatalf("expected JSON object, got %T", value)
	}
	return object
}

func frameData(t *testing.T, frame []byte) string {
	t.Helper()
	text := string(frame)
	const marker = "data: "
	index := strings.Index(text, marker)
	if index < 0 {
		t.Fatalf("unexpected frame %q", text)
	}
	return strings.TrimSuffix(text[index+len(marker):], "\n\n")
}

func framePayload(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	return decodeJSON(t, frameData(t, frame))
}

func TestChatParseRequest(t *testing.T) {
	t.Parallel()

	body := decodeJSON(t, `{
		"model": "m",
		"messages": [
			{"role": "system", "content": "sys"},
			{"role": "developer", "content": [{"type": "text", "text": "dev"}]},
			{"role": "user", "content": [
				{"type": "text", "text": "hi"},
				{"type": "image_url", "image_url": {"url": "https://img"}},
				{"type": "video_url", "video_url": {"url": "https://vid"}}
			]},
			{"role": "assistant", "content": "answer", "reasoning_content": "why",
			 "tool_calls": [{"id": "call_1", "function": {"name": "f", "arguments": "{\"a\":1}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "result"}
		],
		"max_completion_tokens": 64,
		"max_tokens": 32,
		"temperature": 0.5,
		"top_p": 0.9,
		"top_k": 7,
		"stop": ["x", 1],
		"tools": [
			{"type": "function", "function": {"name": "f", "description": "d", "parameters": {"type": "object"}}},
			{"type": "other"}
		],
		"tool_choice": {"type": "function", "function": {"name": "f"}},
		"stream": true,
		"stream_options": {"include_usage": true},
		"n": 1,
		"service_tier": "auto",
		"_internal": true,
		"nil_value": null
	}`)

	request, err := Chat{}.ParseRequest(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(request.System) != 2 || request.System[0].Text != "sys" || request.System[1].Text != "dev" {
		t.Fatalf("system = %+v", request.System)
	}
	if len(request.Messages) != 3 {
		t.Fatalf("messages = %d", len(request.Messages))
	}

	user := request.Messages[0]
	if user.Role != ir.RoleUser || len(user.Content) != 3 {
		t.Fatalf("user = %+v", user)
	}
	if user.Content[1].ImageURL != "https://img" || user.Content[2].VideoURL != "https://vid" {
		t.Fatalf("multimodal = %+v", user.Content)
	}

	assistant := request.Messages[1]
	if assistant.Content[0].Type != ir.Thinking || assistant.Content[0].Text != "why" {
		t.Fatalf("thinking = %+v", assistant.Content[0])
	}
	if assistant.Content[2].Type != ir.ToolUse || assistant.Content[2].ToolInput != `{"a":1}` {
		t.Fatalf("tool use = %+v", assistant.Content[2])
	}

	toolResult := request.Messages[2]
	if toolResult.Content[0].Type != ir.ToolResult || toolResult.Content[0].ToolUseID != "call_1" ||
		toolResult.Content[0].Text != "result" {
		t.Fatalf("tool result = %+v", toolResult)
	}

	if request.MaxTokens == nil || *request.MaxTokens != 64 {
		t.Fatalf("max tokens = %v", request.MaxTokens)
	}
	if request.Temperature == nil || *request.Temperature != 0.5 {
		t.Fatalf("temperature = %v", request.Temperature)
	}
	if request.TopK == nil || *request.TopK != 7 {
		t.Fatalf("top k = %v", request.TopK)
	}
	if !reflect.DeepEqual(request.Stop, []string{"x"}) {
		t.Fatalf("stop = %v", request.Stop)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "f" {
		t.Fatalf("tools = %+v", request.Tools)
	}
	if request.ToolChoice == nil || *request.ToolChoice != "tool:f" {
		t.Fatalf("tool choice = %v", request.ToolChoice)
	}
	if !request.Stream || !request.IncludeUsage {
		t.Fatalf("stream flags = %v %v", request.Stream, request.IncludeUsage)
	}
	if request.Extra["service_tier"] != "auto" {
		t.Fatalf("extra = %+v", request.Extra)
	}
	if _, ok := request.Extra["n"]; ok {
		t.Fatalf("n is same-protocol only, extra = %+v", request.Extra)
	}
	if _, ok := request.Extra["_internal"]; ok {
		t.Fatal("underscore key leaked into extra")
	}
	if _, ok := request.Extra["nil_value"]; ok {
		t.Fatal("null value leaked into extra")
	}
}

func TestChatFormatRequest(t *testing.T) {
	t.Parallel()

	maxtokens := 50
	maxDefault := 999
	choice := "tool:f"
	request := &ir.Request{
		System: []ir.Block{
			ir.TextBlock("one"),
			ir.TextBlock("two"),
		},
		Messages: []ir.Message{
			{
				Role: ir.RoleAssistant,
				Content: []ir.Block{
					{Type: ir.Thinking, Text: "t1"},
					{Type: ir.Thinking, Text: "t2"},
					ir.TextBlock("answer"),
					{Type: ir.ToolUse, ToolUseID: "c1", ToolName: "f", ToolInput: `{"a":1}`},
				},
			},
			{
				Role: ir.RoleUser,
				Content: []ir.Block{
					{Type: ir.ToolResult, ToolUseID: "c1", Text: "res"},
					{Type: ir.Image, MediaType: "image/png", Data: "AAAA"},
					ir.TextBlock("caption"),
				},
			},
			{Role: ir.RoleAssistant, Content: []ir.Block{{Type: ir.Thinking}}},
		},
		MaxTokens:    &maxtokens,
		Temperature:  floatPtr(0.2),
		TopP:         floatPtr(0.8),
		TopK:         intPtr(5),
		Stop:         []string{"end"},
		Tools:        []ir.Tool{{Name: "f"}},
		ToolChoice:   &choice,
		Stream:       true,
		IncludeUsage: true,
		Extra:        map[string]any{"service_tier": "auto", "thinking": map[string]any{"type": "enabled"}},
	}

	body, err := Chat{}.FormatRequest(request, "upstream", &maxDefault)
	if err != nil {
		t.Fatalf("format: %v", err)
	}

	if body["model"] != "upstream" {
		t.Fatalf("model = %v", body["model"])
	}
	if body["max_tokens"] != 50 {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}
	messages, ok := util.AsSlice(body["messages"])
	if !ok || len(messages) != 4 {
		t.Fatalf("messages = %v", body["messages"])
	}

	system := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "one\ntwo" {
		t.Fatalf("system message = %v", system)
	}

	assistant := messages[1].(map[string]any)
	if assistant["content"] != "answer" {
		t.Fatalf("assistant content = %v", assistant["content"])
	}
	if assistant["reasoning_content"] != "t1\nt2" {
		t.Fatalf("reasoning = %v", assistant["reasoning_content"])
	}
	calls, _ := util.AsSlice(assistant["tool_calls"])
	if len(calls) != 1 {
		t.Fatalf("tool calls = %v", assistant["tool_calls"])
	}
	call := calls[0].(map[string]any)
	if call["id"] != "c1" {
		t.Fatalf("tool id = %v", call["id"])
	}

	toolMessage := messages[2].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "c1" || toolMessage["content"] != "res" {
		t.Fatalf("tool message = %v", toolMessage)
	}

	if body["stream"] != true {
		t.Fatalf("stream = %v", body["stream"])
	}
	if options, ok := util.AsMap(body["stream_options"]); !ok || options["include_usage"] != true {
		t.Fatalf("stream options = %v", body["stream_options"])
	}
	if _, ok := body["top_k"]; ok {
		t.Fatal("top_k must not be sent to Chat")
	}
	if body["service_tier"] != "auto" {
		t.Fatalf("extra not forwarded: %v", body["service_tier"])
	}
	if _, ok := body["thinking"]; ok {
		t.Fatal("Anthropic-only key forwarded to Chat")
	}
	if body["tool_choice"] == nil {
		t.Fatal("tool choice missing")
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatal("unexpected reasoning_effort")
	}
}

func TestChatFormatRequestUserPartsAndToolOnlyAssistant(t *testing.T) {
	t.Parallel()

	request := &ir.Request{
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.Block{
					ir.TextBlock("look"),
					{Type: ir.Image, ImageURL: "https://img"},
					{Type: ir.Video, VideoURL: "https://vid"},
				},
			},
			{
				Role:    ir.RoleAssistant,
				Content: []ir.Block{{Type: ir.ToolUse, ToolUseID: "c", ToolName: "f", ToolInput: ""}},
			},
		},
	}

	body, err := Chat{}.FormatRequest(request, "upstream", nil)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	messages := body["messages"].([]any)
	user := messages[0].(map[string]any)
	parts, ok := util.AsSlice(user["content"])
	if !ok || len(parts) != 3 {
		t.Fatalf("user content = %v", user["content"])
	}
	if parts[1].(map[string]any)["type"] != "image_url" || parts[2].(map[string]any)["type"] != "video_url" {
		t.Fatalf("user parts = %v", parts)
	}

	assistant := messages[1].(map[string]any)
	if assistant["content"] != nil {
		t.Fatalf("assistant content should be null, got %v", assistant["content"])
	}
	calls := assistant["tool_calls"].([]any)
	if calls[0].(map[string]any)["function"].(map[string]any)["arguments"] != "{}" {
		t.Fatalf("default arguments = %v", calls[0])
	}
}

func TestChatFormatRequestMaxTokensFallback(t *testing.T) {
	t.Parallel()

	fallback := 500
	body, err := Chat{}.FormatRequest(&ir.Request{}, "m", &fallback)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if body["max_tokens"] != 500 {
		t.Fatalf("fallback = %v", body["max_tokens"])
	}

	zero := 0
	body, _ = Chat{}.FormatRequest(&ir.Request{MaxTokens: &zero}, "m", &fallback)
	if body["max_tokens"] != 500 {
		t.Fatalf("zero should fall back = %v", body["max_tokens"])
	}

	body, _ = Chat{}.FormatRequest(&ir.Request{}, "m", nil)
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("unexpected max tokens: %v", body["max_tokens"])
	}

	explicit := 50
	body, _ = Chat{}.FormatRequest(&ir.Request{MaxTokens: &explicit}, "m", &fallback)
	if body["max_tokens"] != 50 {
		t.Fatalf("explicit max tokens = %v", body["max_tokens"])
	}

	body, _ = Chat{}.FormatRequest(&ir.Request{
		Extra: map[string]any{"max_completion_tokens": 50},
	}, "m", &fallback)
	if body["max_completion_tokens"] != 500 {
		t.Fatalf("completion token fallback = %v", body["max_completion_tokens"])
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("max_tokens must not be sent alongside max_completion_tokens")
	}
}

func TestChatParseResponse(t *testing.T) {
	t.Parallel()

	payload := decodeJSON(t, `{
		"id": "chatcmpl-1",
		"model": "up",
		"created": 123,
		"choices": [{"finish_reason": "tool_calls", "message": {
			"role": "assistant",
			"reasoning_content": "think",
			"content": "hello",
			"tool_calls": [{"function": {"name": "f", "arguments": "{}"}}]
		}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 4, "prompt_tokens_details": {"cached_tokens": 3}}
	}`)

	response, err := Chat{}.ParseResponse(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if response.ID != "chatcmpl-1" || response.Created != 123 {
		t.Fatalf("response = %+v", response)
	}
	if response.StopReason == nil || *response.StopReason != ir.ToolUseStop {
		t.Fatalf("stop reason = %v", response.StopReason)
	}
	if len(response.Content) != 3 {
		t.Fatalf("content = %+v", response.Content)
	}
	if response.Content[0].Type != ir.Thinking || response.Content[1].Text != "hello" {
		t.Fatalf("content order = %+v", response.Content)
	}
	if response.Content[2].ToolUseID != "call_0" {
		t.Fatalf("tool id fallback = %q", response.Content[2].ToolUseID)
	}
	if response.Usage.InputTokens != 7 || response.Usage.CacheReadInputTokens != 3 || response.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestChatFormatResponse(t *testing.T) {
	t.Parallel()

	stop := ir.Refusal
	response := &ir.Response{
		Content:    []ir.Block{ir.TextBlock("hi"), {Type: ir.ToolUse, ToolUseID: "c", ToolName: "f"}},
		StopReason: &stop,
	}
	body := Chat{}.FormatResponse(response, "gateway-model")

	if body["model"] != "gateway-model" {
		t.Fatalf("model = %v", body["model"])
	}
	if id, ok := body["id"].(string); !ok || !strings.HasPrefix(id, "chatcmpl-") {
		t.Fatalf("id = %v", body["id"])
	}
	choice := body["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "content_filter" {
		t.Fatalf("finish reason = %v", choice["finish_reason"])
	}
	message := choice["message"].(map[string]any)
	if message["content"] != "hi" {
		t.Fatalf("content = %v", message["content"])
	}

	empty := Chat{}.FormatResponse(&ir.Response{}, "m")
	emptyMessage := empty["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if emptyMessage["content"] != "" {
		t.Fatalf("empty content = %v", emptyMessage["content"])
	}

	toolOnly := Chat{}.FormatResponse(&ir.Response{
		Content: []ir.Block{{Type: ir.ToolUse, ToolName: "f"}},
	}, "m")
	toolMessage := toolOnly["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if toolMessage["content"] != nil {
		t.Fatalf("tool-only content should be null, got %v", toolMessage["content"])
	}
}

func TestChatFormatError(t *testing.T) {
	t.Parallel()

	body := Chat{}.FormatError(400, "bad", "", "", "req")
	errorObject := body["error"].(map[string]any)
	if errorObject["type"] != "invalid_request_error" || errorObject["code"] != nil || errorObject["param"] != nil {
		t.Fatalf("error = %v", errorObject)
	}
	if errorObject["message"] != "bad" {
		t.Fatalf("message = %v", errorObject["message"])
	}
}

func TestChatStreamDecoder(t *testing.T) {
	t.Parallel()

	decoder := Chat{}.NewDecoder()

	chunks := []string{
		`{"id":"id1","model":"up","created":5,"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c1","function":{"name":"f","arguments":"{\"a\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"function":{"arguments":":1}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`[DONE]`,
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

	if events[0].Type != ir.MessageStart || events[0].MessageID != "id1" || events[0].Created != 5 {
		t.Fatalf("first event = %+v", events[0])
	}

	var starts, deltas []ir.StreamEvent
	for _, event := range events {
		switch event.Type {
		case ir.ContentBlockStart:
			starts = append(starts, event)
		case ir.ContentBlockDelta:
			deltas = append(deltas, event)
		}
	}
	if len(starts) != 3 || starts[0].Index != thinkingBlockIndex || starts[1].Index != textBlockIndex || starts[2].Index != protocols.ToolBlockIndexBase+2 {
		t.Fatalf("starts = %+v", starts)
	}
	if starts[2].Block == nil || starts[2].Block.ToolUseID != "c1" || starts[2].Block.ToolName != "f" {
		t.Fatalf("tool start = %+v", starts[2])
	}
	if len(deltas) != 4 || deltas[0].DeltaType != ir.DeltaThinking || deltas[1].DeltaType != ir.DeltaText {
		t.Fatalf("deltas = %+v", deltas)
	}

	last := events[len(events)-1]
	if last.Type != ir.MessageStop {
		t.Fatalf("last event = %+v", last)
	}
	delta := events[len(events)-2]
	if delta.Type != ir.MessageDelta || delta.StopReason == nil || *delta.StopReason != ir.ToolUseStop {
		t.Fatalf("message delta = %+v", delta)
	}
	if delta.Usage == nil || delta.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", delta.Usage)
	}
}

func TestChatStreamDecoderIgnoresBadDataAndErrors(t *testing.T) {
	t.Parallel()

	decoder := Chat{}.NewDecoder()
	if events, err := decoder.Feed("", "not json"); err != nil || len(events) != 0 {
		t.Fatalf("invalid json = %v, %v", events, err)
	}
	if events, err := decoder.Feed("", ""); err != nil || len(events) != 0 {
		t.Fatalf("empty = %v, %v", events, err)
	}

	events, err := decoder.Feed("", `{"error":{"message":"boom","type":"rate_limit_error"}}`)
	if err != nil || len(events) != 1 || events[0].Type != ir.StreamError {
		t.Fatalf("error event = %+v, %v", events, err)
	}
	if events[0].Error != "boom" || events[0].ErrorType != "rate_limit_error" {
		t.Fatalf("error payload = %+v", events[0])
	}
}

func TestChatStreamEncoder(t *testing.T) {
	t.Parallel()

	encoder := Chat{}.NewEncoder("gateway-model", true)

	startFrames, err := encoder.Encode(ir.StreamEvent{Type: ir.MessageStart, MessageID: "up-id", Created: 9})
	if err != nil || len(startFrames) != 1 {
		t.Fatalf("start frames = %d, %v", len(startFrames), err)
	}
	first := framePayload(t, startFrames[0])
	choice := first["choices"].([]any)[0].(map[string]any)
	if choice["delta"].(map[string]any)["role"] != "assistant" {
		t.Fatalf("start chunk = %v", first)
	}
	if first["id"] != "up-id" || first["model"] != "gateway-model" {
		t.Fatalf("chunk header = %v", first)
	}
	if created, ok := util.AsInt(first["created"]); !ok || created != 9 {
		t.Fatalf("chunk created = %v", first["created"])
	}

	textFrames, _ := encoder.Encode(ir.StreamEvent{Type: ir.ContentBlockDelta, DeltaType: ir.DeltaText, Delta: "hi"})
	if len(textFrames) != 1 {
		t.Fatalf("text frames = %d", len(textFrames))
	}
	textPayload := framePayload(t, textFrames[0])
	textChoice := textPayload["choices"].([]any)[0].(map[string]any)
	if textChoice["delta"].(map[string]any)["content"] != "hi" {
		t.Fatalf("text chunk = %v", textPayload)
	}

	toolFrames, _ := encoder.Encode(ir.StreamEvent{
		Type:  ir.ContentBlockStart,
		Index: 1002,
		Block: &ir.Block{Type: ir.ToolUse, ToolUseID: "c1", ToolName: "f"},
	})
	toolPayload := framePayload(t, toolFrames[0])
	toolCall := toolPayload["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if index, ok := util.AsInt(toolCall["index"]); !ok || index != 0 {
		t.Fatalf("tool slot = %v", toolCall["index"])
	}

	stopReason := ir.MaxTokens
	deltaFrames, _ := encoder.Encode(ir.StreamEvent{
		Type:       ir.MessageDelta,
		StopReason: &stopReason,
		Usage:      &ir.Usage{InputTokens: 3, OutputTokens: 4},
	})
	finishPayload := framePayload(t, deltaFrames[0])
	if finishPayload["choices"].([]any)[0].(map[string]any)["finish_reason"] != "length" {
		t.Fatalf("finish chunk = %v", finishPayload)
	}

	stopFrames, _ := encoder.Encode(ir.StreamEvent{Type: ir.MessageStop})
	if len(stopFrames) != 2 {
		t.Fatalf("stop frames = %d", len(stopFrames))
	}
	if frameData(t, stopFrames[1]) != "[DONE]" {
		t.Fatalf("done frame = %q", stopFrames[1])
	}
	usagePayload := framePayload(t, stopFrames[0])
	if total, ok := util.AsInt(usagePayload["usage"].(map[string]any)["total_tokens"]); !ok || total != 7 {
		t.Fatalf("usage chunk = %v", usagePayload)
	}
	if choices, ok := util.AsSlice(usagePayload["choices"]); !ok || len(choices) != 0 {
		t.Fatalf("usage choices = %v", usagePayload["choices"])
	}

	if frames := encoder.Finish(); len(frames) != 0 {
		t.Fatalf("finish after done returned %d frames", len(frames))
	}
}

func TestChatStreamEncoderErrorStopsFurtherOutput(t *testing.T) {
	t.Parallel()

	encoder := Chat{}.NewEncoder("m", false)
	frames, err := encoder.Encode(ir.StreamEvent{Type: ir.StreamError, Error: "boom", ErrorType: "api_error"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(frames) != 3 || frameData(t, frames[2]) != "[DONE]" {
		t.Fatalf("frames = %d", len(frames))
	}
	errorPayload := framePayload(t, frames[1])
	if errorPayload["error"].(map[string]any)["message"] != "boom" {
		t.Fatalf("error frame = %v", errorPayload)
	}

	if frames := encoder.Finish(); len(frames) != 0 {
		t.Fatalf("finish after error = %d", len(frames))
	}
}

func FuzzChatParseRequest(f *testing.F) {
	f.Add(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
	f.Add(`{"messages":"bad"}`)
	f.Add(`{}`)
	f.Fuzz(func(t *testing.T, body string) {
		value, err := util.Decode([]byte(body))
		if err != nil {
			return
		}
		object, ok := util.AsMap(value)
		if !ok {
			return
		}
		_, _ = Chat{}.ParseRequest(object)
		_, _ = Chat{}.ParseResponse(object)
	})
}

func floatPtr(value float64) *float64 { return &value }

func intPtr(value int) *int { return &value }
