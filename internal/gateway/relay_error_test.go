package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// A converted stream must not end silently when the upstream sends a chunk that
// is not JSON: the caller receives the inbound protocol's terminal error event
// instead of a truncated reply that looks complete.
func TestConvertedStreamEmitsErrorEventOnCorruptChunk(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
				Status:        http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"id\":\"chatcmpl-1\",\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
						"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" +
						"data: {not json\n\n")),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/messages", "key-a",
		`{"model":"model/a","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	body := readBody(t, response)

	if !strings.Contains(body, "event: error") {
		t.Fatalf("stream did not end with a terminal error event:\n%s", body)
	}
	if !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("terminal error event has no error envelope:\n%s", body)
	}
	if strings.Contains(body, "event: message_stop") {
		t.Fatalf("stream reported a clean stop after a corrupt chunk:\n%s", body)
	}
}

// A passthrough stream keeps its bytes, but an interrupted upstream still has to
// terminate with the caller's error event.
func TestPassthroughStreamEmitsErrorEventOnReadFailure(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
				Status:        http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(io.MultiReader(
					strings.NewReader("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"),
					errReader{err: io.ErrUnexpectedEOF},
				)),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	body := readBody(t, response)

	if !strings.Contains(body, `"content":"hi"`) {
		t.Fatalf("relayed bytes were lost:\n%s", body)
	}
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "unexpected EOF") {
		t.Fatalf("interrupted stream did not report the failure:\n%s", body)
	}
}

// errReader fails every read after the reader in front of it is exhausted.
type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}
