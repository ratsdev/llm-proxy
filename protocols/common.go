package protocols

import (
	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

// ParseUsage converts an OpenAI-family usage object into IR usage.
//
// Chat Completions uses prompt/completion keys while the Responses API uses
// input/output; the detail keys follow the same prefix.
func ParseUsage(raw map[string]any, promptKey, completionKey string) ir.Usage {
	if raw == nil {
		return ir.Usage{}
	}
	prompt, _ := util.AsInt(raw[promptKey])
	completion, _ := util.AsInt(raw[completionKey])

	cached := 0
	if details, ok := util.AsMap(raw[promptKey+"_details"]); ok {
		cached, _ = util.AsInt(details["cached_tokens"])
	}
	reasoning := 0
	if details, ok := util.AsMap(raw[completionKey+"_details"]); ok {
		reasoning, _ = util.AsInt(details["reasoning_tokens"])
	}

	return ir.Usage{
		InputTokens:          max(prompt-cached, 0),
		OutputTokens:         completion,
		CacheReadInputTokens: cached,
		ReasoningTokens:      reasoning,
	}
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
