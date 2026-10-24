package ir

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
