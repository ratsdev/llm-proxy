package ir

import (
	"encoding/json"
	"testing"
)

func TestUsageMerged(t *testing.T) {
	t.Parallel()

	base := Usage{InputTokens: 10, OutputTokens: 0, CacheReadInputTokens: 4}
	merged := base.Merged(&Usage{OutputTokens: 7})
	if merged.InputTokens != 10 || merged.CacheReadInputTokens != 4 || merged.OutputTokens != 7 {
		t.Fatalf("merged = %+v", merged)
	}
	if merged.PromptTokens() != 14 {
		t.Fatalf("prompt tokens = %d", merged.PromptTokens())
	}
	if merged.TotalTokens() != 21 {
		t.Fatalf("total tokens = %d", merged.TotalTokens())
	}
	if got := base.Merged(nil); got != base {
		t.Fatalf("nil merge changed usage: %+v", got)
	}
}

func TestEffortBudgetRoundTrip(t *testing.T) {
	t.Parallel()

	if EffortForBudget(1000) != "low" || EffortForBudget(1024) != "low" {
		t.Fatal("low budget mapping")
	}
	if EffortForBudget(2048) != "medium" || EffortForBudget(4096) != "high" {
		t.Fatal("medium/high budget mapping")
	}
	if EffortForBudget(8192) != "xhigh" || EffortForBudget(20000) != "max" {
		t.Fatal("xhigh/max budget mapping")
	}
	if budget, ok := BudgetForEffort("minimal"); !ok || budget != 1024 {
		t.Fatalf("minimal = %d, %v", budget, ok)
	}
	if _, ok := BudgetForEffort("unknown"); ok {
		t.Fatal("unknown effort must not resolve")
	}
}

func TestEncryptedReasoningSignature(t *testing.T) {
	t.Parallel()

	signature := EncodeReasoningSignature("cipher")
	if !IsEncryptedReasoning(signature) {
		t.Fatal("signature should be encrypted reasoning")
	}
	if DecodeReasoningSignature(signature) != "cipher" {
		t.Fatal("decode mismatch")
	}
	if IsEncryptedReasoning("real-signature") {
		t.Fatal("plain signature detected as encrypted")
	}
}

func TestJoinText(t *testing.T) {
	t.Parallel()

	blocks := []Block{
		TextBlock("hello"),
		{Type: Video, VideoURL: "https://example.test/v.mp4"},
		TextBlock("world"),
	}
	if got := JoinText(blocks); got != "hello\nworld" {
		t.Fatalf("JoinText = %q", got)
	}
}

func TestReasoningHintFrom(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		extra map[string]any
		want  ReasoningHint
	}{
		{"responses object", map[string]any{"reasoning": map[string]any{"effort": "high"}},
			ReasoningHint{Effort: "high", Raw: map[string]any{"effort": "high"}}},
		{"responses string", map[string]any{"reasoning": "medium"}, ReasoningHint{Effort: "medium"}},
		{"chat string", map[string]any{"reasoning_effort": "low"}, ReasoningHint{Effort: "low"}},
		{"chat object", map[string]any{"reasoning_effort": map[string]any{"effort": "xhigh", "summary": "auto"}},
			ReasoningHint{Effort: "xhigh", Raw: map[string]any{"effort": "xhigh", "summary": "auto"}}},
		{"explicit none", map[string]any{"reasoning_effort": "none"}, ReasoningHint{Effort: "none"}},
		{"thinking disabled", map[string]any{"thinking": map[string]any{"type": "disabled"}},
			ReasoningHint{Disabled: true}},
		{"thinking budget", map[string]any{"thinking": map[string]any{"budget_tokens": json.Number("3000")}},
			ReasoningHint{Effort: "high"}},
		{"empty", map[string]any{}, ReasoningHint{}},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := ReasoningHintFrom(test.extra)
			if got.Effort != test.want.Effort || got.Disabled != test.want.Disabled ||
				(got.Raw == nil) != (test.want.Raw == nil) {
				t.Fatalf("ReasoningHintFrom = %+v, want %+v", got, test.want)
			}
		})
	}
}
