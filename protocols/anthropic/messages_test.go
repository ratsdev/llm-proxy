package anthropic

import (
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/ir"
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
		t.Fatalf("expected object, got %T", value)
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

func TestParseRequest(t *testing.T) {
	t.Parallel()

	body := decodeJSON(t, `{
		"model": "m",
		"system": [{"type": "text", "text": "sys"}, {"type": "image", "source": {"type": "url", "url": "https://x"}}],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hi"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "AAA"}},
				{"type": "image", "source": {"type": "url", "url": "https://img"}}
			]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "why", "signature": "sig"},
				{"type": "redacted_thinking", "data": "opaque"},
				{"type": "tool_use", "id": "toolu_1", "name": "f", "input": {"a": 1}},
				{"type": "text", "text": "ok"}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "done"}], "is_error": true}
			]},
			{"role": "system", "content": "ignored"}
		],
		"tools": [{"name": "f", "description": "d", "input_schema": {"type": "object"}}, {"name": ""}],
		"stop_sequences": ["x", 3],
		"max_tokens": 128,
		"temperature": 0.3,
		"top_p": 0.7,
		"top_k": 9,
		"stream": true,
		"tool_choice": {"type": "any"},
		"metadata": {"user_id": "u"},
		"other": "dropped"
	}`)

	request, err := Messages{}.ParseRequest(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(request.System) != 1 || request.System[0].Text != "sys" {
		t.Fatalf("system = %+v", request.System)
	}
	if len(request.Messages) != 3 {
		t.Fatalf("messages = %d", len(request.Messages))
	}
	user := request.Messages[0]
	if len(user.Content) != 3 || user.Content[1].MediaType != "image/jpeg" || user.Content[2].ImageURL != "https://img" {
		t.Fatalf("user content = %+v", user.Content)
	}
	assistant := request.Messages[1]
	if assistant.Content[0].Text != "why" || assistant.Content[0].Signature != "sig" {
		t.Fatalf("thinking = %+v", assistant.Content[0])
	}
	if assistant.Content[1].Signature != "opaque" || assistant.Content[1].Text != "" {
		t.Fatalf("redacted thinking = %+v", assistant.Content[1])
	}
	if assistant.Content[2].ToolInput != `{"a":1}` {
		t.Fatalf("tool input = %q", assistant.Content[2].ToolInput)
	}
	result := request.Messages[2].Content[0]
	if result.Type != ir.ToolResult || result.Text != "done" || !result.IsError || result.ToolUseID != "toolu_1" {
		t.Fatalf("tool result = %+v", result)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "f" {
		t.Fatalf("tools = %+v", request.Tools)
	}
	if len(request.Stop) != 1 || request.Stop[0] != "x" {
		t.Fatalf("stop = %v", request.Stop)
	}
	if request.MaxTokens == nil || *request.MaxTokens != 128 {
		t.Fatalf("max tokens = %v", request.MaxTokens)
	}
	if request.ToolChoice == nil || *request.ToolChoice != "required" {
		t.Fatalf("tool choice = %v", request.ToolChoice)
	}
	if !request.Stream || !request.IncludeUsage {
		t.Fatalf("stream flags = %v %v", request.Stream, request.IncludeUsage)
	}
	if request.Extra["metadata"] == nil {
		t.Fatalf("extra = %+v", request.Extra)
	}
	if _, ok := request.Extra["other"]; ok {
		t.Fatal("non-whitelisted key leaked into extra")
	}
}

func TestParseToolChoiceVariants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"auto", `{"type":"auto"}`, "auto"},
		{"none", `{"type":"none"}`, "none"},
		{"any", `{"type":"any"}`, "required"},
		{"tool", `{"type":"tool","name":"f"}`, "tool:f"},
		{"tool without name", `{"type":"tool"}`, ""},
		{"unknown", `{"type":"other"}`, ""},
		{"not object", `"auto"`, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value, err := util.Decode([]byte(test.raw))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			choice := parseToolChoice(value)
			if test.want == "" {
				if choice != nil {
					t.Fatalf("choice = %v", *choice)
				}
				return
			}
			if choice == nil || *choice != test.want {
				t.Fatalf("choice = %v, want %q", choice, test.want)
			}
		})
	}
}

func TestFormatRequest(t *testing.T) {
	t.Parallel()

	maxtokens := 2048
	topK := 5
	choice := "tool:f"
	request := &ir.Request{
		System: []ir.Block{ir.TextBlock("sys")},
		Messages: []ir.Message{
			{Role: ir.RoleUser, Content: []ir.Block{
				{Type: ir.ToolResult, ToolUseID: "c1", Text: "res"},
				ir.TextBlock("caption"),
			}},
			{Role: ir.RoleUser, Content: []ir.Block{ir.TextBlock("more")}},
			{Role: ir.RoleAssistant, Content: []ir.Block{
				{Type: ir.Thinking, Text: "thought"},
				{Type: ir.Thinking, Text: "dropped", Signature: "litellm_encrypted_reasoning:abc"},
				{Type: ir.Thinking, Signature: "opaque"},
				{Type: ir.Thinking},
				{Type: ir.Image, ImageURL: "data:image/webp;base64,QUJD"},
				{Type: ir.ToolUse, ToolName: "f", ToolInput: `{"a":1}`},
			}},
		},
		MaxTokens:  &maxtokens,
		TopP:       floatPtr(0.9),
		TopK:       &topK,
		Stop:       []string{"end"},
		Tools:      []ir.Tool{{Name: "f"}},
		ToolChoice: &choice,
		Stream:     true,
		Extra:      map[string]any{"reasoning": map[string]any{"effort": "high"}},
	}

	body, err := Messages{}.FormatRequest(request, "upstream", nil)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if body["model"] != "upstream" || body["max_tokens"] != 2048 {
		t.Fatalf("header = %v", body)
	}
	if body["system"] != "sys" {
		t.Fatalf("system = %v", body["system"])
	}

	messages := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %d", len(messages))
	}
	user := messages[0].(map[string]any)
	userContent := user["content"].([]any)
	if len(userContent) != 3 {
		t.Fatalf("user blocks = %d", len(userContent))
	}
	if userContent[0].(map[string]any)["type"] != "tool_result" {
		t.Fatalf("tool result = %v", userContent[0])
	}

	assistant := messages[1].(map[string]any)
	blocks := assistant["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("assistant blocks = %d: %v", len(blocks), blocks)
	}
	if blocks[0].(map[string]any)["type"] != "redacted_thinking" {
		t.Fatalf("redacted thinking = %v", blocks[0])
	}
	image := blocks[1].(map[string]any)["source"].(map[string]any)
	if image["type"] != "base64" || image["media_type"] != "image/webp" || image["data"] != "QUJD" {
		t.Fatalf("image source = %v", image)
	}
	toolUse := blocks[2].(map[string]any)
	if !strings.HasPrefix(toolUse["id"].(string), "toolu_") {
		t.Fatalf("tool id = %v", toolUse["id"])
	}
	if toolUse["input"].(map[string]any)["a"] == nil {
		t.Fatalf("tool input = %v", toolUse["input"])
	}

	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != 2047 {
		t.Fatalf("thinking = %v", thinking)
	}
	if body["tool_choice"].(map[string]any)["type"] != "tool" {
		t.Fatalf("tool choice = %v", body["tool_choice"])
	}
	if body["stream"] != true {
		t.Fatalf("stream = %v", body["stream"])
	}
	if body["top_k"] != 5 {
		t.Fatalf("top_k = %v", body["top_k"])
	}
}

func TestFormatRequestMaxTokensDefaults(t *testing.T) {
	t.Parallel()

	body, err := Messages{}.FormatRequest(&ir.Request{}, "m", nil)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if body["max_tokens"] != 4096 {
		t.Fatalf("default max tokens = %v", body["max_tokens"])
	}

	fallback := 900
	body, _ = Messages{}.FormatRequest(&ir.Request{}, "m", &fallback)
	if body["max_tokens"] != 900 {
		t.Fatalf("fallback max tokens = %v", body["max_tokens"])
	}

	zero := 0
	body, _ = Messages{}.FormatRequest(&ir.Request{MaxTokens: &zero}, "m", &fallback)
	if body["max_tokens"] != 900 {
		t.Fatalf("zero max tokens should fall back: %v", body["max_tokens"])
	}

	small := 1000
	body, _ = Messages{}.FormatRequest(&ir.Request{
		MaxTokens: &small,
		Extra:     map[string]any{"reasoning_effort": "high"},
	}, "m", nil)
	if _, ok := body["thinking"]; ok {
		t.Fatalf("thinking must be dropped when max_tokens is too small: %v", body["thinking"])
	}
}

func TestParseResponse(t *testing.T) {
	t.Parallel()

	payload := decodeJSON(t, `{
		"id": "msg_1",
		"model": "up",
		"content": [
			{"type": "thinking", "thinking": "t", "signature": "s"},
			{"type": "text", "text": "hello"},
			{"type": "tool_use", "id": "toolu_1", "name": "f", "input": {"a": 1}}
		],
		"stop_reason": "tool_use",
		"stop_sequence": "END",
		"usage": {"input_tokens": 5, "output_tokens": 2, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 1}
	}`)

	response, err := Messages{}.ParseResponse(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if response.ID != "msg_1" || response.Model != "up" {
		t.Fatalf("response = %+v", response)
	}
	if response.StopReason == nil || *response.StopReason != ir.ToolUseStop {
		t.Fatalf("stop reason = %v", response.StopReason)
	}
	if response.StopSequence == nil || *response.StopSequence != "END" {
		t.Fatalf("stop sequence = %v", response.StopSequence)
	}
	if len(response.Content) != 3 || response.Content[2].ToolInput != `{"a":1}` {
		t.Fatalf("content = %+v", response.Content)
	}
	if response.Usage.InputTokens != 5 || response.Usage.CacheReadInputTokens != 3 || response.Usage.CacheCreationInputTokens != 1 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestFormatResponse(t *testing.T) {
	t.Parallel()

	stop := ir.MaxTokens
	sequence := "STOP"
	response := &ir.Response{
		Content: []ir.Block{
			{Type: ir.Thinking, Text: "t"},
			ir.TextBlock("answer"),
			{Type: ir.ToolUse, ToolUseID: "toolu_1", ToolName: "f"},
		},
		StopReason:   &stop,
		StopSequence: &sequence,
		Usage:        ir.Usage{InputTokens: 3, OutputTokens: 4, ReasoningTokens: 99},
	}

	body := Messages{}.FormatResponse(response, "gateway-model")
	if body["model"] != "gateway-model" || body["type"] != "message" || body["role"] != "assistant" {
		t.Fatalf("response header = %v", body)
	}
	if id := body["id"].(string); !strings.HasPrefix(id, "msg_") {
		t.Fatalf("id = %v", id)
	}
	if body["stop_reason"] != "max_tokens" || body["stop_sequence"] != "STOP" {
		t.Fatalf("stop = %v %v", body["stop_reason"], body["stop_sequence"])
	}
	usage := body["usage"].(map[string]any)
	if usage["input_tokens"] != 3 || usage["output_tokens"] != 4 {
		t.Fatalf("usage = %v", usage)
	}
	if _, ok := usage["reasoning_tokens"]; ok {
		t.Fatal("anthropic usage must not expose reasoning tokens")
	}
	thinking := body["content"].([]any)[0].(map[string]any)
	if _, ok := thinking["signature"]; !ok || thinking["signature"] != nil {
		t.Fatalf("empty thinking signature should be null: %v", thinking)
	}
}

func TestFormatError(t *testing.T) {
	t.Parallel()

	body := Messages{}.FormatError(502, "boom", "upstream_error", "", "req_1")
	if body["type"] != "error" || body["request_id"] != "req_1" {
		t.Fatalf("envelope = %v", body)
	}
	if body["error"].(map[string]any)["type"] != "api_error" {
		t.Fatalf("alias mapping = %v", body["error"])
	}
	if body["error"].(map[string]any)["message"] != "boom" {
		t.Fatalf("message = %v", body["error"])
	}

	notFound := Messages{}.FormatError(404, "missing", "", "", "")
	if notFound["error"].(map[string]any)["type"] != "not_found_error" {
		t.Fatalf("status mapping = %v", notFound)
	}

	unknown := Messages{}.FormatError(418, "teapot", "weird_type", "", "")
	if unknown["error"].(map[string]any)["type"] != "api_error" {
		t.Fatalf("unknown status fallback = %v", unknown)
	}
	if _, ok := unknown["request_id"]; ok {
		t.Fatal("empty request id must be omitted")
	}
}

func TestStreamDecoder(t *testing.T) {
	t.Parallel()

	decoder := Messages{}.NewDecoder()
	steps := []struct {
		event string
		data  string
		want  int
	}{
		{"ping", `{"type":"ping"}`, 0},
		{"", `not-json`, 0},
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","model":"up","usage":{"input_tokens":7,"cache_read_input_tokens":2}}}`, 1},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"t","signature":"s"}}`, 1},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"more"}}`, 1},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"bogus"}}`, 0},
		{"content_block_start", `{"type":"content_block_start","index":9,"content_block":{"type":"unknown"}}`, 1},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`, 1},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":"X"},"usage":{"output_tokens":4}}`, 1},
		{"message_stop", `{"type":"message_stop"}`, 1},
		{"error", `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, 1},
	}

	var events []ir.StreamEvent
	for _, step := range steps {
		produced, err := decoder.Feed(step.event, step.data)
		if err != nil {
			t.Fatalf("feed: %v", err)
		}
		if len(produced) != step.want {
			t.Fatalf("step %s produced %d events, want %d", step.event, len(produced), step.want)
		}
		events = append(events, produced...)
	}

	if events[0].Type != ir.MessageStart || events[0].Usage == nil || events[0].Usage.InputTokens != 7 {
		t.Fatalf("message start = %+v", events[0])
	}
	if events[1].Block == nil || events[1].Block.Type != ir.Thinking || events[1].Block.Signature != "s" {
		t.Fatalf("block start = %+v", events[1])
	}
	if events[2].DeltaType != ir.DeltaThinking || events[2].Delta != "more" {
		t.Fatalf("delta = %+v", events[2])
	}
	unknown := events[3]
	if unknown.Block == nil || unknown.Block.Type != ir.Text || unknown.Index != 9 {
		t.Fatalf("unknown block fallback = %+v", unknown)
	}
	messageDelta := events[5]
	if messageDelta.StopReason == nil || *messageDelta.StopReason != ir.MaxTokens ||
		messageDelta.StopSequence == nil || *messageDelta.StopSequence != "X" {
		t.Fatalf("message delta = %+v", messageDelta)
	}
	if messageDelta.Usage == nil || messageDelta.Usage.OutputTokens != 4 {
		t.Fatalf("message delta usage = %+v", messageDelta.Usage)
	}
	last := events[len(events)-1]
	if last.Type != ir.StreamError || last.Error != "busy" || last.ErrorType != "overloaded_error" {
		t.Fatalf("error event = %+v", last)
	}
	if decoder.Finish() != nil {
		t.Fatal("Finish must not emit events")
	}
}

func TestStreamEncoder(t *testing.T) {
	t.Parallel()

	encoder := Messages{}.NewEncoder("gateway-model", false)

	startFrames, err := encoder.Encode(ir.StreamEvent{Type: ir.MessageStart, MessageID: "msg_up"})
	if err != nil || len(startFrames) != 1 {
		t.Fatalf("start = %d, %v", len(startFrames), err)
	}
	start := framePayload(t, startFrames[0])
	message := start["message"].(map[string]any)
	if message["id"] != "msg_up" || message["model"] != "gateway-model" {
		t.Fatalf("message start = %v", message)
	}
	if len(message["content"].([]any)) != 0 {
		t.Fatalf("content = %v", message["content"])
	}

	blockFrames, _ := encoder.Encode(ir.StreamEvent{
		Type:  ir.ContentBlockStart,
		Index: 1000,
		Block: &ir.Block{Type: ir.ToolUse, ToolUseID: "toolu_1", ToolName: "lookup"},
	})
	block := framePayload(t, blockFrames[0])
	if index, ok := util.AsInt(block["index"]); !ok || index != 0 {
		t.Fatalf("slot index = %v", block["index"])
	}
	if block["content_block"].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("content block = %v", block["content_block"])
	}

	deltaFrames, _ := encoder.Encode(ir.StreamEvent{
		Type:      ir.ContentBlockDelta,
		Index:     1000,
		DeltaType: ir.DeltaInputJSON,
		Delta:     `{"city":"Paris"}`,
	})
	delta := framePayload(t, deltaFrames[0])
	if delta["delta"].(map[string]any)["type"] != "input_json_delta" {
		t.Fatalf("delta = %v", delta)
	}

	stopFrames, _ := encoder.Encode(ir.StreamEvent{Type: ir.ContentBlockStop, Index: 1000})
	stop := framePayload(t, stopFrames[0])
	if index, ok := util.AsInt(stop["index"]); !ok || index != 0 {
		t.Fatalf("stop index = %v", stop["index"])
	}
	if extra, _ := encoder.Encode(ir.StreamEvent{Type: ir.ContentBlockStop, Index: 42}); len(extra) != 0 {
		t.Fatalf("unexpected stop for unknown block: %d", len(extra))
	}

	reason := ir.ToolUseStop
	deltaFrames, _ = encoder.Encode(ir.StreamEvent{
		Type:       ir.MessageDelta,
		StopReason: &reason,
		Usage:      &ir.Usage{InputTokens: 5, OutputTokens: 6},
	})
	messageDelta := framePayload(t, deltaFrames[0])
	if messageDelta["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("stop reason = %v", messageDelta["delta"])
	}
	if output, ok := util.AsInt(messageDelta["usage"].(map[string]any)["output_tokens"]); !ok || output != 6 {
		t.Fatalf("usage = %v", messageDelta["usage"])
	}
	if _, ok := messageDelta["usage"].(map[string]any)["input_tokens"]; ok {
		t.Fatal("message_delta usage must only contain output tokens")
	}

	endFrames, _ := encoder.Encode(ir.StreamEvent{Type: ir.MessageStop})
	if len(endFrames) != 1 || framePayload(t, endFrames[0])["type"] != "message_stop" {
		t.Fatalf("message stop = %d", len(endFrames))
	}
	if frames := encoder.Finish(); len(frames) != 0 {
		t.Fatalf("finish after stop = %d", len(frames))
	}
}

func TestStreamEncoderFinishCompletesStream(t *testing.T) {
	t.Parallel()

	encoder := Messages{}.NewEncoder("m", false)
	frames := encoder.Finish()
	if len(frames) != 3 {
		t.Fatalf("finish frames = %d", len(frames))
	}
	if !strings.Contains(string(frames[0]), "message_start") ||
		!strings.Contains(string(frames[1]), "message_delta") ||
		!strings.Contains(string(frames[2]), "message_stop") {
		t.Fatalf("unexpected finish frames: %q", frames)
	}

	encoder = Messages{}.NewEncoder("m", false)
	if frames, _ := encoder.Encode(ir.StreamEvent{Type: ir.StreamError, Error: "boom"}); len(frames) != 2 {
		t.Fatalf("error frames = %d", len(frames))
	}
	if frames := encoder.Finish(); len(frames) != 0 {
		t.Fatalf("finish after error = %d", len(frames))
	}
}

func TestFormatBlockRedactedAndImages(t *testing.T) {
	t.Parallel()

	if payload, ok := formatBlock(ir.Block{Type: ir.Image, MediaType: "image/png", Data: "AAA"}, false); !ok ||
		payload["source"].(map[string]any)["type"] != "base64" {
		t.Fatalf("base64 image = %v", payload)
	}
	if payload, ok := formatBlock(ir.Block{Type: ir.Image, ImageURL: "https://img"}, false); !ok ||
		payload["source"].(map[string]any)["type"] != "url" {
		t.Fatalf("url image = %v", payload)
	}
	if _, ok := formatBlock(ir.Block{Type: ir.Image}, false); ok {
		t.Fatal("empty image must be dropped")
	}
	if payload, ok := formatBlock(ir.Block{Type: ir.Thinking, Signature: "opaque"}, false); !ok ||
		payload["type"] != "redacted_thinking" {
		t.Fatalf("redacted thinking = %v", payload)
	}
	if _, ok := formatBlock(ir.Block{Type: ir.Thinking, Signature: "litellm_encrypted_reasoning:x"}, true); ok {
		t.Fatal("encrypted reasoning must be dropped on replay")
	}
	if _, ok := formatBlock(ir.Block{Type: ir.Thinking}, true); ok {
		t.Fatal("unsigned thinking must be dropped on replay")
	}
}

func floatPtr(value float64) *float64 { return &value }
