package convert

import (
	"fmt"

	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols/anthropic"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols/openai"
)

// AdapterFor returns the adapter for a wire format, or nil when the format is
// unknown. The registry lives here so the gateway and the conversion entry
// points cannot disagree about which adapters exist.
func AdapterFor(format Format) protocols.Adapter {
	switch format {
	case ChatCompletions:
		return openai.Chat{}
	case Responses:
		return openai.Responses{}
	case Messages:
		return anthropic.Messages{}
	default:
		return nil
	}
}

// Request converts a request body from one wire format to another. The original
// map is never mutated.
func Request(from, to Format, body map[string]any, upstreamModel string) (map[string]any, error) {
	if body == nil {
		return nil, fmt.Errorf("missing request body")
	}
	if from == to {
		out := cloneObject(body)
		out["model"] = upstreamModel
		return out, nil
	}
	source, target := AdapterFor(from), AdapterFor(to)
	if source == nil || target == nil {
		return nil, fmt.Errorf("no converter from %s to %s", from, to)
	}
	request, err := source.ParseRequest(body)
	if err != nil {
		return nil, fmt.Errorf("convert request from %s: %w", from, err)
	}
	converted, err := target.FormatRequest(request, upstreamModel, nil)
	if err != nil {
		return nil, fmt.Errorf("convert request to %s: %w", to, err)
	}
	return converted, nil
}

// Response converts a response body from one wire format to another. The
// original map is never mutated.
func Response(from, to Format, body map[string]any, publicModel string) (map[string]any, error) {
	if body == nil {
		return nil, fmt.Errorf("missing response body")
	}
	if from == to {
		out := cloneObject(body)
		if target := AdapterFor(to); target != nil {
			target.RewriteModel(out, publicModel)
		}
		return out, nil
	}
	source, target := AdapterFor(from), AdapterFor(to)
	if source == nil || target == nil {
		return nil, fmt.Errorf("no converter from %s to %s", from, to)
	}
	response, err := source.ParseResponse(body)
	if err != nil {
		return nil, fmt.Errorf("convert response from %s: %w", from, err)
	}
	return target.FormatResponse(response, publicModel), nil
}

func cloneObject(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for key, value := range body {
		out[key] = cloneValue(value)
	}
	return out
}

// cloneValue copies nested JSON containers so rewriting a nested model name
// cannot mutate the caller's payload.
func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneValue(item)
		}
		return out
	default:
		return value
	}
}

// FormatError renders the error envelope for a wire format.
func FormatError(format Format, status int, message, errorType, code, requestID string) map[string]any {
	target := AdapterFor(format)
	if target == nil {
		return nil
	}
	return target.FormatError(status, message, errorType, code, requestID)
}

// StreamErrorFrames renders a terminal SSE error event for a wire format.
func StreamErrorFrames(format Format, message string) [][]byte {
	target := AdapterFor(format)
	if target == nil {
		return nil
	}
	return target.NewEncoder("", false).EncodeStreamError(message)
}
