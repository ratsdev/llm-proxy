package protocols

import "github.com/ratsdev/llm-proxy/internal/convert/ir"

// ToolBlockIndexBase offsets streamed tool blocks so they do not collide with
// the fixed text (0) and thinking (1) IR indexes used by OpenAI adapters.
const ToolBlockIndexBase = 1000

var chatFinishToCanonical = map[string]string{
	"stop":           ir.EndTurn,
	"length":         ir.MaxTokens,
	"tool_calls":     ir.ToolUseStop,
	"function_call":  ir.ToolUseStop,
	"content_filter": ir.Refusal,
}

var canonicalToChatFinish = map[string]string{
	ir.EndTurn:      "stop",
	ir.MaxTokens:    "length",
	ir.ToolUseStop:  "tool_calls",
	ir.StopSequence: "stop",
	ir.Refusal:      "content_filter",
}

var anthropicStopToCanonical = map[string]string{
	"end_turn":      ir.EndTurn,
	"max_tokens":    ir.MaxTokens,
	"stop_sequence": ir.StopSequence,
	"tool_use":      ir.ToolUseStop,
	"refusal":       ir.Refusal,
	"pause_turn":    ir.EndTurn,
}

var canonicalToAnthropicStop = map[string]string{
	ir.EndTurn:      "end_turn",
	ir.MaxTokens:    "max_tokens",
	ir.StopSequence: "stop_sequence",
	ir.ToolUseStop:  "tool_use",
	ir.Refusal:      "refusal",
}

// ChatFinishToCanonical maps a Chat Completions finish_reason onto IR.
func ChatFinishToCanonical(finish string) string {
	if reason, ok := chatFinishToCanonical[finish]; ok {
		return reason
	}
	return ir.EndTurn
}

// CanonicalToChatFinish maps an IR stop reason onto Chat Completions.
func CanonicalToChatFinish(reason string) string {
	if finish, ok := canonicalToChatFinish[reason]; ok {
		return finish
	}
	return "stop"
}

// AnthropicStopToCanonical maps a Messages stop_reason onto IR.
func AnthropicStopToCanonical(stop string) string {
	if reason, ok := anthropicStopToCanonical[stop]; ok {
		return reason
	}
	return ir.EndTurn
}

// CanonicalToAnthropicStop maps an IR stop reason onto the Messages API.
func CanonicalToAnthropicStop(reason string) string {
	if stop, ok := canonicalToAnthropicStop[reason]; ok {
		return stop
	}
	return "end_turn"
}
