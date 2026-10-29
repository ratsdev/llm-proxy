package protocols

import (
	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// Decoder converts upstream SSE events into IR stream events.
type Decoder interface {
	// Feed consumes one SSE event and returns zero or more IR events.
	Feed(event, data string) ([]ir.StreamEvent, error)
	// Finish closes any blocks still open and emits the terminal events.
	Finish() []ir.StreamEvent
}

// Encoder converts IR stream events into encoded SSE frames.
type Encoder interface {
	// Encode consumes one IR event and returns zero or more SSE frames.
	Encode(ev ir.StreamEvent) ([][]byte, error)
	// EncodeStreamError emits the protocol's error event without starting the
	// stream. It is used when a passthrough stream breaks after data has
	// already been forwarded, where starting the encoder would add a bogus
	// message/response header.
	EncodeStreamError(message string) [][]byte
	// Finish emits any terminal frames that have not been written yet.
	Finish() [][]byte
}

// Adapter converts between one wire format and the IR.
type Adapter interface {
	ParseRequest(body map[string]any) (*ir.Request, error)
	FormatRequest(req *ir.Request, upstreamModel string, maxTokensDefault *int) (map[string]any, error)
	ParseResponse(payload map[string]any) (*ir.Response, error)
	FormatResponse(resp *ir.Response, model string) map[string]any
	FormatError(status int, message, errorType, code, requestID string) map[string]any
	// RewriteModel replaces the public model name wherever the wire format
	// embeds it (top level or nested in message/response envelopes). It
	// reports whether anything changed.
	RewriteModel(payload map[string]any, model string) bool
	NewDecoder() Decoder
	NewEncoder(model string, includeUsage bool) Encoder
}

// FormatUsage renders IR usage for an OpenAI-family wire format.
func FormatUsage(usage ir.Usage, promptKey, completionKey string) map[string]any {
	payload := map[string]any{
		promptKey:      usage.PromptTokens(),
		completionKey:  usage.OutputTokens,
		"total_tokens": usage.TotalTokens(),
	}
	if usage.CacheReadInputTokens != 0 {
		payload[promptKey+"_details"] = map[string]any{
			"cached_tokens": usage.CacheReadInputTokens,
		}
	}
	if usage.ReasoningTokens != 0 {
		payload[completionKey+"_details"] = map[string]any{
			"reasoning_tokens": usage.ReasoningTokens,
		}
	}
	return payload
}

// SlotMap maps IR block indexes onto a wire format's sequential indexes. The IR
// uses stable indexes (text/thinking/tool groups) while wire formats expect
// content blocks numbered 0, 1, 2, ... in emission order.
type SlotMap struct {
	slots map[int]int
}

// Slot returns the sequential index for an IR block index.
func (m *SlotMap) Slot(index int) int {
	if m.slots == nil {
		m.slots = make(map[int]int)
	}
	if slot, ok := m.slots[index]; ok {
		return slot
	}
	slot := len(m.slots)
	m.slots[index] = slot
	return slot
}

// Has reports whether an IR block index has already been assigned a slot.
func (m *SlotMap) Has(index int) bool {
	_, ok := m.slots[index]
	return ok
}

// BlocksFrom normalises a wire "content" value into IR blocks. A bare string
// becomes one text block; arrays are parsed element-wise, dropping parts the
// adapter does not understand.
func BlocksFrom(raw any, parse func(any) (ir.Block, bool)) []ir.Block {
	if text, ok := raw.(string); ok {
		if text == "" {
			return nil
		}
		return []ir.Block{ir.TextBlock(text)}
	}
	items, ok := util.AsSlice(raw)
	if !ok {
		return nil
	}
	blocks := make([]ir.Block, 0, len(items))
	for _, item := range items {
		if block, ok := parse(item); ok {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

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
