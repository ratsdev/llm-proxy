package convert

import (
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/util"
)

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

func TestDefinitionsCoverCrossProtocolPairs(t *testing.T) {
	t.Parallel()

	definitions := Definitions()
	if len(definitions) != 6 {
		t.Fatalf("definitions = %v", definitions)
	}
	for _, definition := range definitions {
		parsed, ok := Lookup(definition.ID)
		if !ok || parsed.From != definition.From || parsed.To != definition.To {
			t.Fatalf("lookup %q = %+v, ok=%v", definition.ID, parsed, ok)
		}
	}
}

func TestDefinitionForRejectsWrongInbound(t *testing.T) {
	t.Parallel()

	if _, err := DefinitionFor(ChatCompletions, ClaudeMessagesToOpenAIChat); err == nil {
		t.Fatal("converter for the wrong inbound format was accepted")
	}
	definition, err := DefinitionFor(ChatCompletions, OpenAIChatToClaudeMessages)
	if err != nil || definition.To != Messages {
		t.Fatalf("definition = %+v, err=%v", definition, err)
	}
	none, err := DefinitionFor(Responses, None)
	if err != nil || none.To != Responses {
		t.Fatalf("none definition = %+v, err=%v", none, err)
	}
}

func TestChatRequestConvertsToMessages(t *testing.T) {
	t.Parallel()

	body := object(t, `{
		"model": "public",
		"messages": [
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "hello"}
		],
		"max_tokens": 64,
		"tools": [{"type": "function", "function": {"name": "f", "parameters": {"type": "object"}}}]
	}`)
	converted, err := Request(ChatCompletions, Messages, body, "claude-upstream")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted["model"] != "claude-upstream" {
		t.Fatalf("model = %v", converted["model"])
	}
	if converted["system"] != "be brief" {
		t.Fatalf("system = %v", converted["system"])
	}
	if maxTokens, _ := util.AsInt(converted["max_tokens"]); maxTokens != 64 {
		t.Fatalf("max_tokens = %v", converted["max_tokens"])
	}
	messages, _ := util.AsSlice(converted["messages"])
	if len(messages) != 1 {
		t.Fatalf("messages = %v", messages)
	}
	tools, _ := util.AsSlice(converted["tools"])
	tool, _ := util.AsMap(tools[0])
	if tool["name"] != "f" || tool["input_schema"] == nil {
		t.Fatalf("tools = %v", tools)
	}
	if body["model"] != "public" {
		t.Fatalf("input body was mutated: %v", body)
	}
}

func TestMessagesResponseConvertsToChat(t *testing.T) {
	t.Parallel()

	body := object(t, `{
		"id": "msg_1",
		"type": "message",
		"role": "assistant",
		"model": "upstream-model",
		"content": [{"type": "text", "text": "hello"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 3, "output_tokens": 2}
	}`)
	converted, err := Response(Messages, ChatCompletions, body, "public")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted["model"] != "public" {
		t.Fatalf("model = %v", converted["model"])
	}
	choices, _ := util.AsSlice(converted["choices"])
	choice, _ := util.AsMap(choices[0])
	message, _ := util.AsMap(choice["message"])
	if message["content"] != "hello" {
		t.Fatalf("message = %v", message)
	}
	usage, _ := util.AsMap(converted["usage"])
	if prompt, _ := util.AsInt(usage["prompt_tokens"]); prompt != 3 {
		t.Fatalf("usage = %v", usage)
	}
	if completion, _ := util.AsInt(usage["completion_tokens"]); completion != 2 {
		t.Fatalf("usage = %v", usage)
	}
}

func TestChatConvertsToResponses(t *testing.T) {
	t.Parallel()

	request := object(t, `{
		"model": "public",
		"messages": [{"role": "user", "content": "hello"}],
		"max_tokens": 32
	}`)
	convertedRequest, err := Request(ChatCompletions, Responses, request, "gpt-upstream")
	if err != nil {
		t.Fatalf("convert request: %v", err)
	}
	if convertedRequest["model"] != "gpt-upstream" {
		t.Fatalf("model = %v", convertedRequest["model"])
	}
	if convertedRequest["input"] == nil {
		t.Fatalf("input missing: %v", convertedRequest)
	}

	response := object(t, `{
		"id": "resp_1", "object": "response", "model": "gpt-upstream",
		"status": "completed",
		"output": [{"type": "message", "role": "assistant",
			"content": [{"type": "output_text", "text": "hi"}]}],
		"usage": {"input_tokens": 4, "output_tokens": 1}
	}`)
	convertedResponse, err := Response(Responses, ChatCompletions, response, "public")
	if err != nil {
		t.Fatalf("convert response: %v", err)
	}
	if convertedResponse["model"] != "public" {
		t.Fatalf("model = %v", convertedResponse["model"])
	}
	choices, _ := util.AsSlice(convertedResponse["choices"])
	if len(choices) != 1 {
		t.Fatalf("choices = %v", choices)
	}
}

func TestResponsesRequestConvertsToMessages(t *testing.T) {
	t.Parallel()

	body := object(t, `{
		"model": "public",
		"instructions": "be brief",
		"input": [{"type": "message", "role": "user",
			"content": [{"type": "input_text", "text": "hello"}]}]
	}`)
	converted, err := Request(Responses, Messages, body, "claude-upstream")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted["system"] != "be brief" {
		t.Fatalf("system = %v", converted["system"])
	}
	if converted["max_tokens"] == nil {
		t.Fatalf("max_tokens missing: %v", converted)
	}
}

func TestIdentityConvertRequestRewritesModelWithoutMutation(t *testing.T) {
	t.Parallel()

	body := map[string]any{
		"model":    "public",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"n":        2,
	}
	converted, err := Request(ChatCompletions, ChatCompletions, body, "upstream-model")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted["model"] != "upstream-model" {
		t.Fatalf("model = %v", converted["model"])
	}
	if _, ok := converted["messages"]; !ok {
		t.Fatalf("messages were dropped: %v", converted)
	}
	if body["model"] != "public" {
		t.Fatalf("input body was mutated: %v", body)
	}
}

func TestIdentityConvertResponseRewritesNestedModel(t *testing.T) {
	t.Parallel()

	body := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    "msg_1",
			"model": "upstream-model",
		},
	}
	converted, err := Response(Messages, Messages, body, "public-model")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	message := converted["message"].(map[string]any)
	if message["model"] != "public-model" {
		t.Fatalf("nested model = %v", message["model"])
	}
	if body["message"].(map[string]any)["model"] != "upstream-model" {
		t.Fatal("input response was mutated")
	}
}

func TestStreamErrorFramesPerFormat(t *testing.T) {
	t.Parallel()

	chat := string(StreamErrorFrames(ChatCompletions, "boom")[0])
	if chat == "" {
		t.Fatal("Chat Completions error frame is empty")
	}
	messages := string(StreamErrorFrames(Messages, "boom")[0])
	if !strings.Contains(messages, "event: error") || !strings.Contains(messages, "boom") {
		t.Fatalf("Messages frame = %q", messages)
	}
	responses := string(StreamErrorFrames(Responses, "boom")[0])
	if !strings.Contains(responses, "event: response.failed") || !strings.Contains(responses, "boom") {
		t.Fatalf("Responses frame = %q", responses)
	}
}
