package gateway

import (
	"net/http"
	"testing"
)

func TestServerIndexAndHealth(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)

	response := doJSON(t, http.MethodGet, server.URL+"/api/", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	if body["name"] != "llm-proxy" || body["version"] != Version {
		t.Fatalf("index = %v", body)
	}
	if got, _ := body["models"].(float64); int(got) != 2 {
		t.Fatalf("models = %v", body["models"])
	}
	endpoints, _ := body["endpoints"].(map[string]any)
	openai, _ := endpoints["chat_completions"].([]any)
	if len(openai) == 0 || openai[0] != "/api/v1/chat/completions" {
		t.Fatalf("advertised chat_completions endpoints = %v", endpoints["chat_completions"])
	}

	bare := doJSON(t, http.MethodGet, server.URL+"/api", "", "")
	if bare.StatusCode != http.StatusOK {
		t.Fatalf("/api status = %d", bare.StatusCode)
	}

	root := doJSON(t, http.MethodGet, server.URL+"/", "", "")
	if root.StatusCode != http.StatusOK {
		t.Fatalf("root status = %d, want 200", root.StatusCode)
	}

	response = doJSON(t, http.MethodGet, server.URL+"/api/health", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	body = decodeBody(t, response)
	if body["status"] != "healthy" {
		t.Fatalf("health body = %v", body)
	}
	if got, _ := body["providers"].(float64); int(got) != 1 {
		t.Fatalf("health providers = %v", body["providers"])
	}
}

func TestServerModelListingRespectsACL(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		cfg.UsersByKey["key-empty"] = &User{ID: "carol"}
	})

	restricted := decodeBody(t, doJSON(t, http.MethodGet, server.URL+"/api/v1/models", "key-a", ""))
	data := restricted["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["id"] != "model/a" {
		t.Fatalf("restricted models = %v", data)
	}

	unrestricted := decodeBody(t, doJSON(t, http.MethodGet, server.URL+"/api/v1/models", "key-b", ""))
	if len(unrestricted["data"].([]any)) != 2 {
		t.Fatalf("wildcard models = %v", unrestricted["data"])
	}

	empty := decodeBody(t, doJSON(t, http.MethodGet, server.URL+"/api/v1/models", "key-empty", ""))
	if len(empty["data"].([]any)) != 0 {
		t.Fatalf("empty allow-list models = %v", empty["data"])
	}
	if response := doJSON(t, http.MethodGet, server.URL+"/api/v1/models/model/a", "key-empty", ""); response.StatusCode != http.StatusForbidden {
		t.Fatalf("empty allow-list detail status = %d", response.StatusCode)
	}

	detail := doJSON(t, http.MethodGet, server.URL+"/api/v1/models/model/a", "key-a", "")
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d", detail.StatusCode)
	}
	detailBody := decodeBody(t, detail)
	if detailBody["id"] != "model/a" || detailBody["object"] != "model" {
		t.Fatalf("detail = %v", detailBody)
	}

	forbidden := doJSON(t, http.MethodGet, server.URL+"/api/v1/models/model-b", "key-a", "")
	if forbidden.StatusCode != http.StatusForbidden {
		t.Fatalf("forbidden status = %d", forbidden.StatusCode)
	}
	if decodeBody(t, forbidden)["error"].(map[string]any)["code"] != "model_not_allowed" {
		t.Fatal("expected model_not_allowed")
	}

	missing := doJSON(t, http.MethodGet, server.URL+"/api/v1/models/unknown", "key-b", "")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.StatusCode)
	}
}

func TestServerRequestErrors(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)

	cases := []struct {
		name       string
		body       string
		path       string
		wantStatus int
		wantCode   string
	}{
		{"invalid json", `{`, "/api/v1/chat/completions", 400, "invalid_json"},
		{"not an object", `[1]`, "/api/v1/chat/completions", 400, "invalid_body"},
		{"missing model", `{}`, "/api/v1/chat/completions", 400, "model_required"},
		{"blank model", `{"model":"  "}`, "/api/v1/chat/completions", 400, "model_required"},
		{"unknown model", `{"model":"nope"}`, "/api/v1/chat/completions", 404, "model_not_found"},
		{"denied model", `{"model":"model-b"}`, "/api/v1/chat/completions", 403, "model_not_allowed"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := doJSON(t, http.MethodPost, server.URL+test.path, "key-a", test.body)
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d", response.StatusCode)
			}
			errorObject := decodeBody(t, response)["error"].(map[string]any)
			if errorObject["code"] != test.wantCode {
				t.Fatalf("code = %v", errorObject["code"])
			}
		})
	}
}

func TestServerUnknownRoute(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	response := doJSON(t, http.MethodGet, server.URL+"/nope", "", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body := decodeBody(t, response)
	if body["detail"] != "Not Found" {
		t.Fatalf("body = %v", body)
	}
}
