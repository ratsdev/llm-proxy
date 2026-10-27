package ir

import (
	"strings"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// EncryptedReasoningPrefix lets Responses "encrypted_content" travel through
// Anthropic thinking signatures so it survives a client round trip. The prefix
// matches the one litellm uses, keeping signatures interoperable.
const EncryptedReasoningPrefix = "litellm_encrypted_reasoning:"

// EncodeReasoningSignature wraps encrypted reasoning content in a signature.
func EncodeReasoningSignature(encryptedContent string) string {
	return EncryptedReasoningPrefix + encryptedContent
}

// IsEncryptedReasoning reports whether signature carries encrypted reasoning.
func IsEncryptedReasoning(signature string) bool {
	return strings.HasPrefix(signature, EncryptedReasoningPrefix)
}

// DecodeReasoningSignature unwraps encrypted reasoning content.
func DecodeReasoningSignature(signature string) string {
	return strings.TrimPrefix(signature, EncryptedReasoningPrefix)
}

var effortBudgets = map[string]int{
	"minimal": 1024,
	"low":     1024,
	"medium":  2048,
	"high":    4096,
	"xhigh":   8192,
	"max":     16384,
}

var effortOrder = []string{"low", "medium", "high", "xhigh"}

// EffortForBudget approximates the reasoning effort that corresponds to a
// thinking budget.
func EffortForBudget(budget int) string {
	for _, effort := range effortOrder {
		if budget <= effortBudgets[effort] {
			return effort
		}
	}
	return "max"
}

// BudgetForEffort returns the thinking budget for an effort name.
func BudgetForEffort(effort string) (int, bool) {
	budget, ok := effortBudgets[effort]
	return budget, ok
}

// ReasoningHint is the protocol-neutral view of a caller's reasoning request.
type ReasoningHint struct {
	// Effort is the requested effort name; "none" is preserved so a caller can
	// explicitly turn reasoning off.
	Effort string
	// Raw carries a full Responses-style reasoning object (effort and/or
	// summary) that must be forwarded verbatim.
	Raw map[string]any
	// Disabled reports an explicit Anthropic thinking.type == "disabled".
	Disabled bool
}

// ReasoningHintFrom normalises the reasoning hints found in a request's extra
// fields. Every adapter maps the result onto its own wire fields so the
// precedence rules live in one place.
func ReasoningHintFrom(extra map[string]any) ReasoningHint {
	hint := ReasoningHint{}
	if reasoning, ok := extra["reasoning"]; ok {
		switch value := reasoning.(type) {
		case map[string]any:
			hint.Raw = value
			hint.Effort = util.StringValue(value["effort"])
		case string:
			hint.Effort = value
		}
	}
	if hint.Effort == "" {
		switch value := extra["reasoning_effort"].(type) {
		case map[string]any:
			if _, hasEffort := value["effort"]; hasEffort {
				hint.Raw = value
			}
			if _, hasSummary := value["summary"]; hasSummary {
				hint.Raw = value
			}
			hint.Effort = util.StringValue(value["effort"])
		case string:
			hint.Effort = value
		}
	}
	if thinking, ok := extra["thinking"].(map[string]any); ok {
		if util.StringValue(thinking["type"]) == "disabled" {
			hint.Disabled = true
		} else if hint.Effort == "" {
			if budget, ok := util.AsInt(thinking["budget_tokens"]); ok && budget > 0 {
				hint.Effort = EffortForBudget(budget)
			}
		}
	}
	return hint
}
