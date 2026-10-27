package ir

import "github.com/ratsdev/llm-proxy/internal/util"

// Usage is the normalised token accounting shared by all protocols.
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	ReasoningTokens          int
}

// PromptTokens is the input size including cache reads and writes. Anthropic
// reports those separately, so the raw input count alone would under-report the
// prompt size.
func (u Usage) PromptTokens() int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// TotalTokens is the prompt plus output token count.
func (u Usage) TotalTokens() int {
	return u.PromptTokens() + u.OutputTokens
}

// Merged combines two usage updates, keeping non-zero values from either side.
// Streams report input tokens in the first event and output tokens in the last
// one, so a later partial update must not reset earlier values.
func (u Usage) Merged(other *Usage) Usage {
	if other == nil {
		return u
	}
	return Usage{
		InputTokens:              firstNonZero(other.InputTokens, u.InputTokens),
		OutputTokens:             firstNonZero(other.OutputTokens, u.OutputTokens),
		CacheReadInputTokens:     firstNonZero(other.CacheReadInputTokens, u.CacheReadInputTokens),
		CacheCreationInputTokens: firstNonZero(other.CacheCreationInputTokens, u.CacheCreationInputTokens),
		ReasoningTokens:          firstNonZero(other.ReasoningTokens, u.ReasoningTokens),
	}
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

// ParseUsage extracts normalised token accounting from a usage object. Both
// OpenAI key families (prompt/completion and input/output) and Anthropic's
// separate cache fields are understood, so passthrough and converted traffic
// are accounted for by one set of rules.
//
// OpenAI reports cached tokens inside the prompt count while PromptTokens adds
// cache reads separately, so the cached count is subtracted here to avoid
// double counting. Anthropic already reports input_tokens without cache reads.
func ParseUsage(raw map[string]any) (Usage, bool) {
	if len(raw) == 0 {
		return Usage{}, false
	}
	usage := Usage{}
	if _, ok := raw["prompt_tokens"]; ok {
		usage.InputTokens = intValue(raw["prompt_tokens"])
		usage.OutputTokens = intValue(raw["completion_tokens"])
		if details, ok := util.AsMap(raw["prompt_tokens_details"]); ok {
			cached := intValue(details["cached_tokens"])
			usage.CacheReadInputTokens = cached
			usage.InputTokens = max(0, usage.InputTokens-cached)
		}
		if details, ok := util.AsMap(raw["completion_tokens_details"]); ok {
			usage.ReasoningTokens = intValue(details["reasoning_tokens"])
		}
	} else {
		usage.InputTokens = intValue(raw["input_tokens"])
		usage.OutputTokens = intValue(raw["output_tokens"])
		if details, ok := util.AsMap(raw["input_tokens_details"]); ok {
			cached := intValue(details["cached_tokens"])
			usage.CacheReadInputTokens = cached
			usage.InputTokens = max(0, usage.InputTokens-cached)
		}
		if details, ok := util.AsMap(raw["output_tokens_details"]); ok {
			usage.ReasoningTokens = intValue(details["reasoning_tokens"])
		}
	}
	if cached, ok := util.AsInt(raw["cache_read_input_tokens"]); ok {
		usage.CacheReadInputTokens = cached
	}
	if created, ok := util.AsInt(raw["cache_creation_input_tokens"]); ok {
		usage.CacheCreationInputTokens = created
	}
	return usage, true
}

// UsageFrom is ParseUsage for an arbitrary JSON value. A missing or malformed
// usage field yields zero usage, which is what a protocol that omits usage on
// some events expects.
func UsageFrom(raw any) Usage {
	object, ok := util.AsMap(raw)
	if !ok {
		return Usage{}
	}
	usage, _ := ParseUsage(object)
	return usage
}

func intValue(value any) int {
	number, _ := util.AsInt(value)
	return number
}
