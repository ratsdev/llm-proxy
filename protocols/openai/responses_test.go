package openai

import (
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/protocols"
	"github.com/ratsdev/llm-proxy/util"
)

func TestResponsesParseRequest(t *testing.T) {
	t.Parallel()

	body := decodeJSON(t, `{
		"model": "m",
		"instructions": "be nice",
		"input": [
			{"role": "system", "content": [{"type": "input_text", "text": "sys"}]},
			"hello",
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hi"}]},
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "S1"}, {"type": "summary_text", "text": "S2"}], "encrypted_content": "enc"},
			{"type": "function_call", "call_id": "c1", "name": "f", "arguments": "{\"a\":1}"},
			{"type": "function_call_output", "call_id": "c1", "output": [{"type": "input_text", "text": "ok"}]}
		],
		"tools": [
			{"type": "function", "name": "f", "parameters": {"type": "object"}},
			{"type": "function", "function": {"name": "g"}},
			{"type": "other"}
		],
		"tool_choice": {"type": "function", "name": "f"},
		"max_output_tokens": 100,
		"max_tokens": 50,
		"stream": true,
		"reasoning": {"effort": "high"},
		"store": true,
		"other": "dropped"
	}`)

	request, err := Responses{}.ParseRequest(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(request.System) != 2 || request.System[0].Text != "be nice" || request.System[1].Text != "sys" {
		t.Fatalf("system = %+v", request.System)
	}
	if len(request.Messages) != 3 {
		t.Fatalf("messages = %d: %+v", len(request.Messages), request.Messages)
	}
	if request.Messages[0].Role != ir.RoleUser || request.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("first message = %+v", request.Messages[0])
	}

	assistant := request.Messages[1]
	if len(assistant.Content) != 3 {
		t.Fatalf("assistant blocks = %+v", assistant.Content)
	}
	thinking := assistant.Content[1]
	if thinking.Type != ir.Thinking || thinking.Text != "S1\n\nS2" ||
		thinking.Signature != "litellm_encrypted_reasoning:enc" {
		t.Fatalf("thinking = %+v", thinking)
	}
	toolUse := assistant.Content[2]
	if toolUse.Type != ir.ToolUse || toolUse.ToolUseID != "c1" || toolUse.ToolInput != `{"a":1}` {
		t.Fatalf("tool use = %+v", toolUse)
	}

	toolResult := request.Messages[2]
	if toolResult.Role != ir.RoleUser || toolResult.Content[0].Type != ir.ToolResult ||
		toolResult.Content[0].Text != "ok" || toolResult.Content[0].ToolUseID != "c1" {
		t.Fatalf("tool result = %+v", toolResult)
	}

	if len(request.Tools) != 2 || request.Tools[0].Name != "f" || request.Tools[1].Name != "g" {
		t.Fatalf("tools = %+v", request.Tools)
	}
	if request.ToolChoice == nil || *request.ToolChoice != "tool:f" {
		t.Fatalf("tool choice = %v", request.ToolChoice)
	}
	if request.MaxTokens == nil || *request.MaxTokens != 100 {
		t.Fatalf("max tokens = %v", request.MaxTokens)
	}
	if !request.Stream || !request.IncludeUsage {
		t.Fatalf("stream flags = %v %v", request.Stream, request.IncludeUsage)
	}
	if request.Extra["reasoning"] == nil || request.Extra["store"] != true {
		t.Fatalf("extra = %+v", request.Extra)
	}
	if _, ok := request.Extra["other"]; ok {
		t.Fatal("non-whitelisted key leaked into extra")
	}
}

func TestResponsesParseInputString(t *testing.T) {
	t.Parallel()

	request, err := Responses{}.ParseRequest(decodeJSON(t, `{"model":"m","input":"hello"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(request.Messages) != 1 || request.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("messages = %+v", request.Messages)
	}

	empty, _ := Responses{}.ParseRequest(decodeJSON(t, `{"model":"m","input":""}`))
	if len(empty.Messages) != 0 {
		t.Fatalf("empty input = %+v", empty.Messages)
	}
}

func TestResponsesFormatRequest(t *testing.T) {
	t.Parallel()

	maxTokens := 100
	fallback := 500
	choice := "tool:f"
	request := &ir.Request{
		System: []ir.Block{ir.TextBlock("sys")},
		Messages: []ir.Message{
			{
				Role: ir.RoleAssistant,
				Content: []ir.Block{
					{Type: ir.Thinking, Text: "r1"},
					{Type: ir.Thinking, Text: "r2"},
					ir.TextBlock("answer"),
					{Type: ir.ToolUse, ToolUseID: "c1", ToolName: "f", ToolInput: ""},
					{Type: ir.Thinking, Signature: "litellm_encrypted_reasoning:enc"},
					ir.TextBlock("tail"),
				},
			},
			{
				Role: ir.RoleUser,
				Content: []ir.Block{
					{Type: ir.ToolResult, ToolUseID: "c1", Text: "res"},
					ir.TextBlock("caption"),
					{Type: ir.Image, MediaType: "image/png", Data: "AAA"},
				},
			},
		},
		MaxTokens:  &maxTokens,
		TopP:       floatPtr(0.8),
		Tools:      []ir.Tool{{Name: "f"}},
		ToolChoice: &choice,
		Stream:     true,
		Extra: map[string]any{
			"reasoning":   map[string]any{"effort": "high"},
			"store":       true,
			"not_allowed": true,
		},
	}

	body, err := Responses{}.FormatRequest(request, "upstream", &fallback)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if body["model"] != "upstream" || body["instructions"] != "sys" {
		t.Fatalf("header = %v", body)
	}
	if body["max_output_tokens"] != 100 {
		t.Fatalf("max output tokens = %v", body["max_output_tokens"])
	}

	items := body["input"].([]any)
	if len(items) != 7 {
		t.Fatalf("items = %d: %v", len(items), items)
	}
	first := items[0].(map[string]any)
	if first["type"] != "reasoning" {
		t.Fatalf("first item = %v", first)
	}
	summary := first["summary"].([]any)
	if len(summary) != 2 || summary[0].(map[string]any)["text"] != "r1" {
		t.Fatalf("summary = %v", summary)
	}
	if items[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "answer" {
		t.Fatalf("message item = %v", items[1])
	}
	call := items[2].(map[string]any)
	if call["type"] != "function_call" || call["arguments"] != "{}" || call["call_id"] != "c1" {
		t.Fatalf("function call = %v", call)
	}
	encrypted := items[3].(map[string]any)
	if encrypted["encrypted_content"] != "enc" {
		t.Fatalf("encrypted reasoning = %v", encrypted)
	}
	if len(encrypted["summary"].([]any)) != 0 {
		t.Fatalf("encrypted summary = %v", encrypted["summary"])
	}
	if items[4].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "tail" {
		t.Fatalf("tail item = %v", items[4])
	}

	toolOutput := items[5].(map[string]any)
	if toolOutput["type"] != "function_call_output" || toolOutput["output"] != "res" {
		t.Fatalf("tool output = %v", toolOutput)
	}
	userParts := items[6].(map[string]any)["content"].([]any)
	if len(userParts) != 2 || userParts[0].(map[string]any)["type"] != "input_text" ||
		userParts[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("user parts = %v", userParts)
	}
	if !strings.HasPrefix(userParts[1].(map[string]any)["image_url"].(string), "data:image/png;base64,") {
		t.Fatalf("image url = %v", userParts[1])
	}
	if body["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatalf("reasoning = %v", body["reasoning"])
	}
	if body["store"] != true {
		t.Fatalf("extra store = %v", body["store"])
	}
	if _, ok := body["not_allowed"]; ok {
		t.Fatal("non-whitelisted key forwarded")
	}
	if body["tool_choice"].(map[string]any)["name"] != "f" {
		t.Fatalf("tool choice = %v", body["tool_choice"])
	}
}

func TestResponsesFormatRequestMaxTokensFallback(t *testing.T) {
	t.Parallel()

	fallback := 500
	body, _ := Responses{}.FormatRequest(&ir.Request{}, "m", &fallback)
	if body["max_output_tokens"] != 500 {
		t.Fatalf("fallback = %v", body["max_output_tokens"])
	}

	zero := 0
	body, _ = Responses{}.FormatRequest(&ir.Request{MaxTokens: &zero}, "m", &fallback)
	if body["max_output_tokens"] != 500 {
		t.Fatalf("zero should fall back = %v", body["max_output_tokens"])
	}

	body, _ = Responses{}.FormatRequest(&ir.Request{}, "m", nil)
	if _, ok := body["max_output_tokens"]; ok {
		t.Fatalf("unexpected max output tokens: %v", body["max_output_tokens"])
	}
}

func TestResponsesParseResponse(t *testing.T) {
	t.Parallel()

	payload := decodeJSON(t, `{
		"id": "resp_1",
		"model": "up",
		"created_at": 42,
		"status": "completed",
		"output": [
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "a"}, {"type": "summary_text", "text": "b"}], "encrypted_content": "enc"},
			{"type": "message", "content": [{"type": "output_text", "text": "hello"}]},
			{"type": "function_call", "call_id": "c1", "name": "f", "arguments": "{}"}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5, "input_tokens_details": {"cached_tokens": 2}}
	}`)

	response, err := Responses{}.ParseResponse(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if response.ID != "resp_1" || response.Created != 42 {
		t.Fatalf("response = %+v", response)
	}
	if len(response.Content) != 3 {
		t.Fatalf("content = %+v", response.Content)
	}
	if response.Content[0].Text != "a\n\nb" || response.Content[0].Signature != "litellm_encrypted_reasoning:enc" {
		t.Fatalf("thinking = %+v", response.Content[0])
	}
	if response.Content[1].Text != "hello" {
		t.Fatalf("text = %+v", response.Content[1])
	}
	if response.StopReason == nil || *response.StopReason != ir.ToolUseStop {
		t.Fatalf("stop reason = %v", response.StopReason)
	}
	if response.Usage.InputTokens != 8 || response.Usage.CacheReadInputTokens != 2 || response.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestResponsesStopReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"completed", `{"status":"completed"}`, ir.EndTurn},
		{"max output", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, ir.MaxTokens},
		{"content filter", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, ir.Refusal},
		{"tool use wins", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, ir.ToolUseStop},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload := decodeJSON(t, test.payload)
			content := []ir.Block{}
			if test.want == ir.ToolUseStop {
				content = append(content, ir.Block{Type: ir.ToolUse})
			}
			if got := responseStopReason(payload, content); got != test.want {
				t.Fatalf("stop reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResponsesFormatResponse(t *testing.T) {
	t.Parallel()

	stop := ir.MaxTokens
	response := &ir.Response{
		Content: []ir.Block{
			{Type: ir.Thinking, Text: "r"},
			{Type: ir.Thinking, Signature: "litellm_encrypted_reasoning:enc"},
			ir.TextBlock("a"),
			ir.TextBlock("b"),
			{Type: ir.ToolUse, ToolUseID: "c1", ToolName: "f", ToolInput: "{}"},
			ir.TextBlock("c"),
		},
		StopReason: &stop,
		Usage:      ir.Usage{InputTokens: 3, OutputTokens: 4},
	}

	body := Responses{}.FormatResponse(response, "gateway-model")
	if id := body["id"].(string); !strings.HasPrefix(id, "resp_") {
		t.Fatalf("id = %v", id)
	}
	if body["object"] != "response" || body["model"] != "gateway-model" {
		t.Fatalf("header = %v", body)
	}
	if body["status"] != "incomplete" || body["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("status = %v", body)
	}

	output := body["output"].([]any)
	if len(output) != 5 {
		t.Fatalf("output = %d: %v", len(output), output)
	}
	if output[0].(map[string]any)["type"] != "reasoning" {
		t.Fatalf("reasoning = %v", output[0])
	}
	if output[1].(map[string]any)["encrypted_content"] != "enc" {
		t.Fatalf("encrypted reasoning = %v", output[1])
	}
	if output[2].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "ab" {
		t.Fatalf("message = %v", output[2])
	}
	if output[3].(map[string]any)["type"] != "function_call" {
		t.Fatalf("function call = %v", output[3])
	}
	if output[4].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "c" {
		t.Fatalf("tail = %v", output[4])
	}

	encrypted := output[0].(map[string]any)
	if encrypted["encrypted_content"] != nil {
		t.Fatalf("first reasoning must be plain: %v", encrypted)
	}
	usage := body["usage"].(map[string]any)
	if usage["input_tokens"] != 3 || usage["output_tokens"] != 4 {
		t.Fatalf("usage = %v", usage)
	}
}

func TestResponsesFormatErrorSharesOpenAIEnvelope(t *testing.T) {
	t.Parallel()

	body := Responses{}.FormatError(429, "slow down", "rate_limit_error", "rate", "req")
	errorObject := body["error"].(map[string]any)
	if errorObject["type"] != "rate_limit_error" || errorObject["code"] != "rate" {
		t.Fatalf("error = %v", errorObject)
	}
	if _, ok := errorObject["param"]; !ok {
		t.Fatalf("param must be present: %v", errorObject)
	}
}

func TestResponsesDecoder(t *testing.T) {
	t.Parallel()

	decoder := Responses{}.NewDecoder()
	steps := []struct {
		event string
		data  string
		want  int
	}{
		{"response.created", `{"type":"response.created","response":{"id":"resp_1","model":"up","created_at":7}}`, 1},
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning"}}`, 1},
		{"response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"t1"}`, 1},
		{"response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","output_index":0,"delta":"dup"}`, 0},
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"type":"message"}}`, 1},
		{"response.output_text.delta", `{"type":"response.output_text.delta","output_index":1,"delta":"hi"}`, 1},
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"c1","name":"f"}}`, 1},
		{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{}"}`, 1},
		{"response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","encrypted_content":"enc"}}`, 2},
		{"response.completed", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":4}}}`, 4},
	}

	var events []ir.StreamEvent
	for _, step := range steps {
		produced, err := decoder.Feed(step.event, step.data)
		if err != nil {
			t.Fatalf("feed %s: %v", step.event, err)
		}
		if len(produced) != step.want {
			t.Fatalf("step %s produced %d events, want %d", step.event, len(produced), step.want)
		}
		events = append(events, produced...)
	}

	if events[0].Type != ir.MessageStart || events[0].MessageID != "resp_1" || events[0].Created != 7 {
		t.Fatalf("message start = %+v", events[0])
	}
	if events[1].Block == nil || events[1].Block.Type != ir.Thinking {
		t.Fatalf("reasoning block = %+v", events[1])
	}
	if events[2].DeltaType != ir.DeltaThinking || events[2].Delta != "t1" {
		t.Fatalf("thinking delta = %+v", events[2])
	}
	if events[3].Block == nil || events[3].Block.Type != ir.Text {
		t.Fatalf("message block = %+v", events[3])
	}
	toolStart := events[5]
	if toolStart.Index != protocols.ToolBlockIndexBase+2 || toolStart.Block.ToolUseID != "c1" {
		t.Fatalf("tool start = %+v", toolStart)
	}
	if events[6].DeltaType != ir.DeltaInputJSON {
		t.Fatalf("arguments delta = %+v", events[6])
	}
	if events[7].DeltaType != ir.DeltaSignature || events[7].Delta != "litellm_encrypted_reasoning:enc" {
		t.Fatalf("signature delta = %+v", events[7])
	}
	if events[8].Type != ir.ContentBlockStop {
		t.Fatalf("block stop = %+v", events[8])
	}

	last := events[len(events)-1]
	delta := events[len(events)-2]
	if last.Type != ir.MessageStop {
		t.Fatalf("last = %+v", last)
	}
	if delta.Type != ir.MessageDelta || delta.StopReason == nil || *delta.StopReason != ir.ToolUseStop {
		t.Fatalf("message delta = %+v", delta)
	}
	if delta.Usage == nil || delta.Usage.InputTokens != 3 || delta.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", delta.Usage)
	}
	if decoder.Finish() != nil {
		t.Fatal("finish after completion must be empty")
	}
}

func TestResponsesDecoderCompletesWithoutCreated(t *testing.T) {
	t.Parallel()

	decoder := Responses{}.NewDecoder()
	events, err := decoder.Feed("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"m","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2}}}`)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(events) != 2 || events[0].Type != ir.MessageDelta || events[1].Type != ir.MessageStop {
		t.Fatalf("events = %+v", events)
	}
}

func TestResponsesDecoderFailure(t *testing.T) {
	t.Parallel()

	decoder := Responses{}.NewDecoder()
	events, err := decoder.Feed("response.failed", `{"type":"response.failed","response":{"error":{"type":"server_error","message":"boom"}}}`)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(events) != 1 || events[0].Type != ir.StreamError || events[0].Error != "boom" {
		t.Fatalf("events = %+v", events)
	}
	if decoder.Finish() != nil {
		t.Fatal("finish after failure must be empty")
	}
}

func TestResponsesEncoder(t *testing.T) {
	t.Parallel()

	encoder := Responses{}.NewEncoder("gateway-model", false)
	startFrames, err := encoder.Encode(ir.StreamEvent{Type: ir.MessageStart, MessageID: "resp_up", Created: 9})
	if err != nil || len(startFrames) != 1 {
		t.Fatalf("start = %d, %v", len(startFrames), err)
	}
	start := framePayload(t, startFrames[0])
	if seq, ok := util.AsInt(start["sequence_number"]); !ok || seq != 0 {
		t.Fatalf("sequence = %v", start["sequence_number"])
	}
	response := start["response"].(map[string]any)
	if response["id"] != "resp_up" || response["model"] != "gateway-model" {
		t.Fatalf("response = %v", response)
	}

	blockFrames, _ := encoder.Encode(ir.StreamEvent{
		Type:  ir.ContentBlockStart,
		Index: 1000,
		Block: &ir.Block{Type: ir.ToolUse, ToolUseID: "c1", ToolName: "lookup"},
	})
	if len(blockFrames) != 1 {
		t.Fatalf("tool block frames = %d", len(blockFrames))
	}
	added := framePayload(t, blockFrames[0])
	if index, ok := util.AsInt(added["output_index"]); !ok || index != 0 {
		t.Fatalf("output index = %v", added["output_index"])
	}
	item := added["item"].(map[string]any)
	if item["type"] != "function_call" || item["call_id"] != "c1" {
		t.Fatalf("item = %v", item)
	}

	deltaFrames, _ := encoder.Encode(ir.StreamEvent{
		Type:      ir.ContentBlockDelta,
		Index:     1000,
		DeltaType: ir.DeltaInputJSON,
		Delta:     `{"city":"Paris"}`,
	})
	delta := framePayload(t, deltaFrames[0])
	if delta["type"] != "response.function_call_arguments.delta" {
		t.Fatalf("delta = %v", delta)
	}

	stopFrames, _ := encoder.Encode(ir.StreamEvent{Type: ir.ContentBlockStop, Index: 1000})
	if len(stopFrames) != 2 {
		t.Fatalf("stop frames = %d", len(stopFrames))
	}
	done := framePayload(t, stopFrames[1])
	doneItem := done["item"].(map[string]any)
	if doneItem["arguments"] != `{"city":"Paris"}` || doneItem["status"] != "completed" {
		t.Fatalf("done item = %v", doneItem)
	}

	reason := ir.ToolUseStop
	if frames, _ := encoder.Encode(ir.StreamEvent{Type: ir.MessageDelta, StopReason: &reason}); len(frames) != 0 {
		t.Fatalf("message delta frames = %d", len(frames))
	}

	completeFrames, _ := encoder.Encode(ir.StreamEvent{Type: ir.MessageStop})
	if len(completeFrames) != 1 {
		t.Fatalf("complete frames = %d", len(completeFrames))
	}
	completed := framePayload(t, completeFrames[0])
	if completed["type"] != "response.completed" {
		t.Fatalf("completed event = %v", completed)
	}
	finalResponse := completed["response"].(map[string]any)
	if finalResponse["status"] != "completed" {
		t.Fatalf("status = %v", finalResponse["status"])
	}
	if len(finalResponse["output"].([]any)) != 1 {
		t.Fatalf("output = %v", finalResponse["output"])
	}
	if encoder.Finish() != nil {
		t.Fatal("finish after completion must be empty")
	}
}

func TestResponsesEncoderIncompleteAndError(t *testing.T) {
	t.Parallel()

	encoder := Responses{}.NewEncoder("m", false)
	reason := ir.MaxTokens
	if frames, _ := encoder.Encode(ir.StreamEvent{Type: ir.MessageDelta, StopReason: &reason}); len(frames) != 1 {
		// The first event lazily starts the stream.
		t.Fatalf("start frames = %d", len(frames))
	}
	frames, _ := encoder.Encode(ir.StreamEvent{Type: ir.MessageStop})
	final := framePayload(t, frames[0])
	if final["type"] != "response.incomplete" {
		t.Fatalf("event = %v", final["type"])
	}
	response := final["response"].(map[string]any)
	if response["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete = %v", response)
	}

	encoder = Responses{}.NewEncoder("m", false)
	frames, _ = encoder.Encode(ir.StreamEvent{Type: ir.StreamError, Error: "boom"})
	last := framePayload(t, frames[len(frames)-1])
	if last["type"] != "response.failed" {
		t.Fatalf("failed event = %v", last)
	}
	failed := last["response"].(map[string]any)
	if failed["status"] != "failed" || failed["error"].(map[string]any)["code"] != "server_error" {
		t.Fatalf("failed response = %v", failed)
	}
	if encoder.Finish() != nil {
		t.Fatal("finish after failure must be empty")
	}
}

func TestResponsesSequenceNumbers(t *testing.T) {
	t.Parallel()

	encoder := Responses{}.NewEncoder("m", false)
	events := []ir.StreamEvent{
		{Type: ir.MessageStart},
		{Type: ir.ContentBlockStart, Index: 0, Block: &ir.Block{Type: ir.Text}},
		{Type: ir.ContentBlockDelta, Index: 0, DeltaType: ir.DeltaText, Delta: "hi"},
		{Type: ir.ContentBlockStop, Index: 0},
		{Type: ir.MessageStop},
	}

	var frames [][]byte
	for _, event := range events {
		produced, err := encoder.Encode(event)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		frames = append(frames, produced...)
	}

	for index, frame := range frames {
		payload := framePayload(t, frame)
		seq, ok := util.AsInt(payload["sequence_number"])
		if !ok || seq != index {
			t.Fatalf("frame %d sequence = %v", index, payload["sequence_number"])
		}
	}
}

func FuzzResponsesParseRequest(f *testing.F) {
	f.Add(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	f.Add(`{"input":"hi"}`)
	f.Add(`{"input":[{"type":"reasoning","summary":[]}]}`)
	f.Fuzz(func(t *testing.T, body string) {
		value, err := util.Decode([]byte(body))
		if err != nil {
			return
		}
		object, ok := util.AsMap(value)
		if !ok {
			return
		}
		_, _ = Responses{}.ParseRequest(object)
		_, _ = Responses{}.ParseResponse(object)
	})
}
