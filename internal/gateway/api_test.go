package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/core"
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

func newTestApp(t *testing.T) (*App, *Store) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app, err := NewApp(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(app.Close)
	return app, store
}

func appHealth(t *testing.T, app *App) (status int, body map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return recorder.Code, nil
	}
	return recorder.Code, decodeBody(t, recorder.Result())
}

// Reload is the hot-swap path: the new stack must be live for requests that
// start after it returns, and the old stack must not be reused.
func TestAppReloadPublishesNewConfiguration(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t)

	status, body := appHealth(t, app)
	if status != http.StatusOK {
		t.Fatalf("initial health status = %d", status)
	}
	if got, _ := body["providers"].(float64); int(got) != 0 {
		t.Fatalf("initial providers = %v", body["providers"])
	}

	if _, err := store.SaveProvider(core.ProviderInput{
		ID:        "upstream",
		BaseURL:   "https://api.example.com/v1",
		Endpoints: map[string]core.ProviderEndpointInput{"chat_completions": {Converter: convert.None}},
	}); err != nil {
		t.Fatalf("save provider: %v", err)
	}
	if _, err := store.SaveModel(core.ModelInput{
		ID:        "public",
		Providers: []core.ModelProviderInput{{ProviderID: "upstream", UpstreamModel: "up-model"}},
	}); err != nil {
		t.Fatalf("save model: %v", err)
	}
	if err := app.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	status, body = appHealth(t, app)
	if status != http.StatusOK {
		t.Fatalf("health after reload = %d", status)
	}
	if got, _ := body["providers"].(float64); int(got) != 1 {
		t.Fatalf("providers after reload = %v", body["providers"])
	}
	if got, _ := body["models"].(float64); int(got) != 1 {
		t.Fatalf("models after reload = %v", body["models"])
	}
	if cfg := app.Config(); len(cfg.Models) != 1 {
		t.Fatalf("config snapshot models = %d", len(cfg.Models))
	}
}

// Reload must reuse the routing memory and the upstream connection pool instead
// of building a cold stack on every administrative write.
func TestAppReloadKeepsRoutingMemory(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t)
	state, pool := app.routing, app.pool
	if err := app.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if app.routing != state {
		t.Fatal("reload replaced the routing state")
	}
	if app.pool != pool {
		t.Fatal("reload replaced the transport pool")
	}
}

// Concurrent administrative writes must not interleave a stale snapshot: every
// reload re-reads the database, and the last one to finish wins.
func TestAppConcurrentReload(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t)

	var wg sync.WaitGroup
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := app.Reload(); err != nil {
				t.Errorf("reload: %v", err)
			}
		}()
	}
	for index := 0; index < 16; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))
			if recorder.Code != http.StatusOK {
				t.Errorf("health during reload = %d", recorder.Code)
			}
		}()
	}
	wg.Wait()
}

// After Close there is no live stack, so requests must fail closed rather than
// reach a half-released router.
func TestAppServeHTTPAfterClose(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t)
	app.Close()

	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after close = %d", recorder.Code)
	}
}

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

func TestReadCapped(t *testing.T) {
	t.Parallel()

	data, exceeded, err := readCapped(strings.NewReader("12345678"), 8)
	if err != nil || exceeded || string(data) != "12345678" {
		t.Fatalf("at limit: data=%q exceeded=%v err=%v", data, exceeded, err)
	}

	data, exceeded, err = readCapped(strings.NewReader("123456789"), 8)
	if err != nil || !exceeded || string(data) != "123456789" {
		t.Fatalf("over limit: data=%q exceeded=%v err=%v", data, exceeded, err)
	}
}
