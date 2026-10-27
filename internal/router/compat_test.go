package router

import (
	"net/http"
	"testing"
)

// Anthropic reports overload as HTTP 529. Gateways retry it and fail over
// because it is a provider-side capacity error, not a client error.
func TestCompatRetriesOverloaded529(t *testing.T) {
	t.Parallel()

	busy := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	})
	live := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	busyProvider := testProvider("p1", busy.URL, "/chat/completions")
	busyProvider.MaxRetries = 0
	liveProvider := testProvider("p2", live.URL, "/chat/completions")
	model := &Model{ID: "m", Providers: []ModelProvider{
		{ProviderID: "p1", UpstreamModel: "u1"},
		{ProviderID: "p2", UpstreamModel: "u2"},
	}}

	router, err := NewRouter(testConfig([]*Provider{busyProvider, liveProvider}, model),
		WithRand(func() float64 { return 0 }), WithSleep(noSleep))
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	defer router.Close()

	result, err := send(t.Context(), router, model, textRequest("hi"), nil, "", "chat_completions")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer result.Close()
	if result.ProviderID != "p2" {
		t.Fatalf("529 was not retried/failed over: provider=%q status=%d", result.ProviderID, result.Status)
	}
}
