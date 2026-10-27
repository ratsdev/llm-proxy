package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func sessionHeader(t *testing.T, name, value string) http.Header {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", nil)
	if name != "" {
		request.Header.Set(name, value)
	}
	return request.Header
}

func TestSessionKeyUsesExplicitHeader(t *testing.T) {
	t.Parallel()

	first := sessionHeader(t, "x-session-id", "conversation-1")
	second := sessionHeader(t, "x-session-id", "conversation-1")
	other := sessionHeader(t, "x-session-id", "conversation-2")

	payload := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "hello"},
	}}
	key := SessionKey(first, payload, "alice", "gpt-4o")
	if key == "" {
		t.Fatal("explicit session header produced an empty key")
	}
	if key != SessionKey(second, payload, "alice", "gpt-4o") {
		t.Fatal("same session header produced different keys")
	}
	if key == SessionKey(other, payload, "alice", "gpt-4o") {
		t.Fatal("different session headers produced the same key")
	}
}

func TestSessionKeyFingerprintsFirstUserMessage(t *testing.T) {
	t.Parallel()

	header := sessionHeader(t, "", "")
	firstTurn := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "be brief"},
		map[string]any{"role": "user", "content": "plan a trip"},
	}}
	secondTurn := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "be brief"},
		map[string]any{"role": "user", "content": "plan a trip"},
		map[string]any{"role": "assistant", "content": "where to?"},
		map[string]any{"role": "user", "content": "kyoto"},
	}}
	different := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "plan a trip to osaka"},
	}}

	key := SessionKey(header, firstTurn, "alice", "gpt-4o")
	if key == "" {
		t.Fatal("first user message produced an empty key")
	}
	if key != SessionKey(header, secondTurn, "alice", "gpt-4o") {
		t.Fatal("later turns changed the session key")
	}
	if key == SessionKey(header, different, "alice", "gpt-4o") {
		t.Fatal("different first messages produced the same key")
	}
	if key == SessionKey(header, firstTurn, "bob", "gpt-4o") {
		t.Fatal("different subjects produced the same key")
	}
	if key == SessionKey(header, firstTurn, "alice", "other-model") {
		t.Fatal("different models produced the same key")
	}
}

func TestSessionKeyPrefersPromptCacheKey(t *testing.T) {
	t.Parallel()

	header := sessionHeader(t, "", "")
	one := map[string]any{
		"prompt_cache_key": "cache-1",
		"messages": []any{
			map[string]any{"role": "user", "content": "first"},
		},
	}
	two := map[string]any{
		"prompt_cache_key": "cache-1",
		"messages": []any{
			map[string]any{"role": "user", "content": "second"},
		},
	}
	if SessionKey(header, one, "alice", "m") != SessionKey(header, two, "alice", "m") {
		t.Fatal("prompt_cache_key did not keep the session stable")
	}
}

func TestSessionKeySupportsResponsesInput(t *testing.T) {
	t.Parallel()

	header := sessionHeader(t, "", "")
	payload := map[string]any{"input": []any{
		map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "describe this image"},
				map[string]any{"type": "input_image", "image_url": "https://example.com/a.png"},
			},
		},
	}}
	key := SessionKey(header, payload, "alice", "vision")
	if key == "" {
		t.Fatal("responses input produced an empty key")
	}
	same := map[string]any{"input": []any{
		map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "describe this image"},
				map[string]any{"type": "input_image", "image_url": "https://example.com/a.png"},
			},
		},
	}}
	if key != SessionKey(header, same, "alice", "vision") {
		t.Fatal("responses input was not stable")
	}
}

func TestSessionKeyWithoutConversationIdentity(t *testing.T) {
	t.Parallel()

	header := sessionHeader(t, "", "")
	if key := SessionKey(header, map[string]any{}, "alice", "m"); key != "" {
		t.Fatalf("empty payload key = %q", key)
	}
}

// A caller-supplied request id must not create affinity: clients that mint a
// unique id per request would otherwise fill the bounded pin cache with one-shot
// entries and evict the conversations that need it.
func TestSessionKeyIgnoresRequestIdentifiers(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", nil)
	request.Header.Set("x-request-id", "req-shared")
	request.Header.Set("idempotency-key", "idem-shared")

	if key := SessionKey(request.Header, map[string]any{}, "alice", "gpt-4o"); key != "" {
		t.Fatalf("SessionKey() = %q, want empty when only request identifiers are present", key)
	}
}
