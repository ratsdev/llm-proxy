// Package protocolstest provides the SSE and JSON helpers shared by the
// protocol adapter tests. It is a regular package rather than a _test file so
// the openai and anthropic test packages can both use it.
package protocolstest

import (
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// DecodeJSON decodes data into a JSON object.
func DecodeJSON(t *testing.T, data string) map[string]any {
	t.Helper()
	value, err := util.Decode([]byte(data))
	if err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	object, ok := util.AsMap(value)
	if !ok {
		t.Fatalf("expected JSON object, got %T", value)
	}
	return object
}

// FrameData returns the data payload of one encoded SSE frame.
func FrameData(t *testing.T, frame []byte) string {
	t.Helper()
	text := string(frame)
	const marker = "data: "
	index := strings.Index(text, marker)
	if index < 0 {
		t.Fatalf("unexpected frame %q", text)
	}
	return strings.TrimSuffix(text[index+len(marker):], "\n\n")
}

// FramePayload decodes the data payload of one encoded SSE frame.
func FramePayload(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	return DecodeJSON(t, FrameData(t, frame))
}

// FloatPtr returns a pointer to value.
func FloatPtr(value float64) *float64 { return &value }

// IntPtr returns a pointer to value.
func IntPtr(value int) *int { return &value }
