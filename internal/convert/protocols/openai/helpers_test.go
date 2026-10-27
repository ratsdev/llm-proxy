package openai

import (
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert/protocols/protocolstest"
)

// The JSON and SSE assertions these helpers make are shared with the other
// adapter packages; only the short local names are kept here so the tests read
// the same way in every protocol.
func decodeJSON(t *testing.T, data string) map[string]any {
	t.Helper()
	return protocolstest.DecodeJSON(t, data)
}

func frameData(t *testing.T, frame []byte) string {
	t.Helper()
	return protocolstest.FrameData(t, frame)
}

func framePayload(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	return protocolstest.FramePayload(t, frame)
}

func floatPtr(value float64) *float64 { return protocolstest.FloatPtr(value) }

func intPtr(value int) *int { return protocolstest.IntPtr(value) }
