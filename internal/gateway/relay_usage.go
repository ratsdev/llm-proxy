package gateway

import (
	"bytes"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// usageFromObject extracts token accounting from a JSON response. The nested
// message/response envelopes used by streaming events are searched too, so
// passthrough traffic still contributes to the Web UI usage summary.
func usageFromObject(object map[string]any) (Usage, bool) {
	if usage, ok := usagePayload(object); ok {
		return usage, true
	}
	for _, nestedKey := range []string{"message", "response"} {
		if nested, ok := util.AsMap(object[nestedKey]); ok {
			if usage, ok := usagePayload(nested); ok {
				return usage, true
			}
		}
	}
	return Usage{}, false
}

// usagePayload reads the usage object of one response body. The parsing rules
// live in the IR so passthrough and converted traffic cannot disagree.
func usagePayload(object map[string]any) (Usage, bool) {
	raw, ok := util.AsMap(object["usage"])
	if !ok {
		return Usage{}, false
	}
	return ir.ParseUsage(raw)
}

// usageFromStreamLine extracts token accounting from one SSE data line. It
// works on bytes so the relay's hot path does not copy every line into a
// string just to look for the "usage" key.
func usageFromStreamLine(line []byte) (Usage, bool) {
	trimmed := bytes.TrimSpace(line)
	const prefix = "data:"
	if !bytes.HasPrefix(trimmed, []byte(prefix)) {
		return Usage{}, false
	}
	payload := bytes.TrimSpace(trimmed[len(prefix):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) ||
		!bytes.Contains(payload, []byte(`"usage"`)) {
		return Usage{}, false
	}
	value, err := util.Decode(payload)
	if err != nil {
		return Usage{}, false
	}
	object, ok := util.AsMap(value)
	if !ok {
		return Usage{}, false
	}
	return usageFromObject(object)
}
