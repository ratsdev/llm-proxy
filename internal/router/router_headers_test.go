package router

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/version"
)

func TestRouterUpstreamHeaders(t *testing.T) {
	t.Parallel()

	var captured atomic.Value
	server := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		captured.Store(r.Header.Clone())
		w.WriteHeader(http.StatusOK)
	})

	openAI := testProvider("openai", server.URL, "/chat/completions")
	openAI.APIKey = "sk-test"
	openAI.Headers = map[string]string{"x-extra": "1", "Authorization": "Custom token"}

	anthropicProvider := testProvider("anthropic", server.URL, "/v1/messages")
	anthropicProvider.APIKey = "ak-test"

	modelFor := func(providerID string) *Model {
		return &Model{ID: "m", Providers: []ModelProvider{{ProviderID: providerID, UpstreamModel: "u"}}}
	}

	openAIModel := modelFor("openai")
	openAIRouter, _ := NewRouter(testConfig([]*Provider{openAI}, openAIModel), WithSleep(noSleep))
	defer openAIRouter.Close()
	result, err := send(t.Context(), openAIRouter, openAIModel, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("openai send: %v", err)
	}
	_ = result.Close()

	headers := captured.Load().(http.Header)
	if headers.Get("Authorization") != "Custom token" {
		t.Fatalf("provider headers must win: %q", headers.Get("Authorization"))
	}
	if headers.Get("x-extra") != "1" {
		t.Fatalf("extra header missing: %v", headers)
	}
	if headers.Get("User-Agent") != "llm-proxy/"+version.Value {
		t.Fatalf("user agent = %q", headers.Get("User-Agent"))
	}
	if headers.Get("Accept") != "application/json" {
		t.Fatalf("accept = %q", headers.Get("Accept"))
	}

	anthropicModel := modelFor("anthropic")
	anthropicRouter, _ := NewRouter(testConfig([]*Provider{anthropicProvider}, anthropicModel), WithSleep(noSleep))
	defer anthropicRouter.Close()
	result, err = send(t.Context(), anthropicRouter, anthropicModel, textRequest("hi"),
		map[string]string{"anthropic-version": "2024-01-01", "anthropic-beta": "beta-1"}, "", "messages")
	if err != nil {
		t.Fatalf("anthropic send: %v", err)
	}
	_ = result.Close()

	headers = captured.Load().(http.Header)
	if headers.Get("x-api-key") != "ak-test" {
		t.Fatalf("x-api-key = %q", headers.Get("x-api-key"))
	}
	if headers.Get("anthropic-version") != "2024-01-01" || headers.Get("anthropic-beta") != "beta-1" {
		t.Fatalf("anthropic headers = %v", headers)
	}
	if headers.Get("Authorization") != "" {
		t.Fatalf("unexpected authorization header: %q", headers.Get("Authorization"))
	}

	// A stream request advertises SSE.
	streamPayload := textRequest("hi")
	streamPayload["stream"] = true
	result, err = send(t.Context(), anthropicRouter, anthropicModel, streamPayload, nil, "", "messages")
	if err != nil {
		t.Fatalf("stream send: %v", err)
	}
	_ = result.Close()
	headers = captured.Load().(http.Header)
	if headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("stream accept = %q", headers.Get("Accept"))
	}
}
