package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestServerMethodNotAllowedUsesJSONEnvelope(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	response := doJSON(t, http.MethodGet, server.URL+"/api/v1/chat/completions", "", "")
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response.Header.Get("Allow") == "" {
		t.Fatal("missing Allow header")
	}
	body := decodeBody(t, response)
	errorObject, ok := body["error"].(map[string]any)
	if !ok || errorObject["code"] != "method_not_allowed" {
		t.Fatalf("body = %v", body)
	}
}

func TestServerAnthropicErrorEnvelope(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return errorResult(model, http.StatusNotFound, `{"error":{"message":"missing","type":"not_found_error"}}`), nil
		},
	}
	server := newTestServer(t, router, nil)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/messages",
		strings.NewReader(`{"model":"model/a","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "key-a")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	if body["type"] != "error" {
		t.Fatalf("envelope = %v", body)
	}
	if body["request_id"] != response.Header.Get("x-request-id") {
		t.Fatalf("request id mismatch: %v vs %q", body["request_id"], response.Header.Get("x-request-id"))
	}
}
