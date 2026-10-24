package llmproxy

import (
	"testing"

	"github.com/ratsdev/llm-proxy/util"
)

func TestUsageFromObject(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		body   string
		want   [3]int // prompt, output, total
		wantOK bool
	}{
		{
			name:   "openai chat",
			body:   `{"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`,
			want:   [3]int{10, 4, 14},
			wantOK: true,
		},
		{
			name:   "openai responses with cached tokens",
			body:   `{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":3}}}`,
			want:   [3]int{10, 4, 14},
			wantOK: true,
		},
		{
			name:   "anthropic nested message with cache",
			body:   `{"message":{"usage":{"input_tokens":5,"output_tokens":6,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`,
			want:   [3]int{8, 6, 14},
			wantOK: true,
		},
		{
			name:   "responses event nested response",
			body:   `{"type":"response.completed","response":{"usage":{"input_tokens":7,"output_tokens":3}}}`,
			want:   [3]int{7, 3, 10},
			wantOK: true,
		},
		{
			name:   "no usage",
			body:   `{"id":"x"}`,
			wantOK: false,
		},
	}

	for _, testCase := range cases {
		value, err := util.Decode([]byte(testCase.body))
		if err != nil {
			t.Fatalf("%s: decode: %v", testCase.name, err)
		}
		object, ok := util.AsMap(value)
		if !ok {
			t.Fatalf("%s: not an object", testCase.name)
		}
		usage, ok := usageFromObject(object)
		if ok != testCase.wantOK {
			t.Fatalf("%s: ok = %v", testCase.name, ok)
		}
		if !ok {
			continue
		}
		got := [3]int{usage.PromptTokens(), usage.OutputTokens, usage.TotalTokens()}
		if got != testCase.want {
			t.Fatalf("%s: usage = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestUsageFromStreamLine(t *testing.T) {
	t.Parallel()

	usage, ok := usageFromStreamLine([]byte(`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	if !ok || usage.PromptTokens() != 3 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, ok = %v", usage, ok)
	}

	for _, line := range []string{
		`event: message`,
		`data: [DONE]`,
		`data: {"id":"x"}`,
		`not sse`,
	} {
		if _, ok := usageFromStreamLine([]byte(line)); ok {
			t.Fatalf("line %q unexpectedly produced usage", line)
		}
	}
}
