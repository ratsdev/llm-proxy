package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestServerAuthentication(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	errorObject := body["error"].(map[string]any)
	if errorObject["code"] != "missing_api_key" || errorObject["type"] != "authentication_error" {
		t.Fatalf("error = %v", errorObject)
	}

	invalid := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "unknown",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if invalid.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid status = %d", invalid.StatusCode)
	}
	if decodeBody(t, invalid)["error"].(map[string]any)["code"] != "invalid_api_key" {
		t.Fatal("expected invalid_api_key")
	}

	// x-api-key also authenticates.
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/chat/completions",
		strings.NewReader(`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", "key-a")
	response2, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response2.Body.Close()
	if response2.StatusCode != http.StatusOK {
		t.Fatalf("x-api-key status = %d", response2.StatusCode)
	}
}

func TestServerAuthDisabledWithoutUsers(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		cfg.UsersByKey = map[string]*User{}
	})
	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
