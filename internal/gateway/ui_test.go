package gateway

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/core"
)

func TestUIPageAndAssets(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)

	for _, path := range []string{"/"} {
		response := uiCall(t, client, http.MethodGet, server.URL+path, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
		if contentType := response.Header.Get("content-type"); !strings.HasPrefix(contentType, "text/html") {
			t.Fatalf("%s content-type = %q", path, contentType)
		}
		if csp := response.Header.Get("content-security-policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Fatalf("%s CSP = %q", path, csp)
		}
		if !strings.Contains(uiBody(t, response), "/assets/app.js") {
			t.Fatalf("%s does not load the app bundle", path)
		}
	}

	assets := map[string]string{
		"/assets/app.js":     "javascript",
		"/assets/styles.css": "text/css",
	}
	for path, wantType := range assets {
		response := uiCall(t, client, http.MethodGet, server.URL+path, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
		if contentType := response.Header.Get("content-type"); !strings.Contains(contentType, wantType) {
			t.Fatalf("%s content-type = %q", path, contentType)
		}
	}

	missing := uiCall(t, client, http.MethodGet, server.URL+"/assets/missing.js", "", nil)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset status = %d", missing.StatusCode)
	}

	index := uiCall(t, client, http.MethodGet, server.URL+"/api", "", nil)
	if body := uiDecode(t, index); body["ui"] != "/" {
		t.Fatalf("index ui = %v", body["ui"])
	}
}

func TestUIAssetRevalidation(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)

	response := uiCall(t, client, http.MethodGet, server.URL+"/assets/app.js", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("asset status = %d", response.StatusCode)
	}
	etag := response.Header.Get("etag")
	if etag == "" {
		t.Fatal("asset response is missing an etag")
	}
	_ = uiBody(t, response)

	cached := uiCall(t, client, http.MethodGet, server.URL+"/assets/app.js", "", map[string]string{
		"If-None-Match": etag,
	})
	if cached.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want %d", cached.StatusCode, http.StatusNotModified)
	}
}

func TestUIRequiresLogin(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)

	for _, path := range []string{"/api/ui/session", "/api/ui/overview", "/api/ui/models", "/api/ui/providers", "/api/ui/requests"} {
		response := uiCall(t, client, http.MethodGet, server.URL+path, "", nil)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
		body := uiDecode(t, response)
		detail, _ := body["error"].(map[string]any)
		if detail["code"] != "not_authenticated" {
			t.Fatalf("%s error = %v", path, body)
		}
	}
}

func TestUILoginSession(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)
	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login",
		`{"username":"`+testAdminUsername+`","password":"`+testAdminPassword+`"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "llm_proxy_ui_session" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", cookie)
	}
	if cookie.Path != "/api/ui" {
		t.Fatalf("cookie path = %q", cookie.Path)
	}
	login := uiDecode(t, response)
	user, _ := login["user"].(map[string]any)
	if user["id"] != testAdminUsername || user["is_admin"] != true {
		t.Fatalf("login user = %v", login)
	}
	loginGateway, _ := login["gateway"].(map[string]any)
	if loginGateway["version"] != Version {
		t.Fatalf("login gateway = %v", login)
	}

	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/session", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d", response.StatusCode)
	}
	session := uiDecode(t, response)
	if session["authenticated"] != true {
		t.Fatalf("session = %v", session)
	}
	sessionUser, _ := session["user"].(map[string]any)
	if sessionUser["id"] != testAdminUsername {
		t.Fatalf("session user = %v", session)
	}

	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/models", "", nil)
	models := uiDecode(t, response)["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("admin sees %d models, want 2", len(models))
	}

	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/providers", "", nil)
	providers := uiDecode(t, response)["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("admin sees %d providers, want 1", len(providers))
	}
	provider := providers[0].(map[string]any)
	if provider["id"] != "provider" {
		t.Fatalf("provider = %v", provider)
	}
	if provider["timeout"] != float64(600) {
		t.Fatalf("provider timeout = %v, want the 600s default", provider["timeout"])
	}
	if models := provider["models"].([]any); len(models) != 2 {
		t.Fatalf("provider models = %v", provider["models"])
	}

	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/overview", "", nil)
	overview := uiDecode(t, response)
	counts, _ := overview["counts"].(map[string]any)
	if counts["models"] != float64(2) || counts["providers"] != float64(1) {
		t.Fatalf("overview counts = %v", counts)
	}
	gateway, _ := overview["gateway"].(map[string]any)
	if gateway["version"] != Version {
		t.Fatalf("overview gateway = %v", gateway)
	}

	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/requests", "", nil)
	requests := uiDecode(t, response)
	if requests["total"] != float64(0) {
		t.Fatalf("requests = %v", requests)
	}
}

func TestUILoginRejectsBadCredentials(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)

	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"wrong password", `{"username":"admin","password":"nope"}`, http.StatusUnauthorized, "invalid_credentials"},
		{"unknown user", `{"username":"nobody","password":"nope"}`, http.StatusUnauthorized, "invalid_credentials"},
		{"api key login", `{"api_key":"key-a"}`, http.StatusBadRequest, "missing_credentials"},
		{"missing credentials", `{}`, http.StatusBadRequest, "missing_credentials"},
		{"blank credentials", `{"username":"   ","password":""}`, http.StatusBadRequest, "missing_credentials"},
		{"invalid json", `{`, http.StatusBadRequest, "invalid_json"},
		{"not an object", `[]`, http.StatusBadRequest, "invalid_json"},
	}
	for _, testCase := range cases {
		response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login", testCase.body, nil)
		if response.StatusCode != testCase.status {
			t.Fatalf("%s status = %d", testCase.name, response.StatusCode)
		}
		body := uiDecode(t, response)
		detail, _ := body["error"].(map[string]any)
		if detail["code"] != testCase.code {
			t.Fatalf("%s error = %v", testCase.name, body)
		}
	}
}

func TestUILogout(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiLogin(t, server)

	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/logout", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", response.StatusCode)
	}
	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/session", "", nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d", response.StatusCode)
	}
}

func TestUIMethodNotAllowed(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiLogin(t, server)

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/api/ui/providers", "{}"},
		{http.MethodDelete, "/api/ui/requests", ""},
		{http.MethodGet, "/api/ui/login", ""},
		{http.MethodDelete, "/api/ui/overview", ""},
	}
	for _, testCase := range cases {
		response := uiCall(t, client, testCase.method, server.URL+testCase.path, testCase.body, nil)
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want %d: %s",
				testCase.method, testCase.path, response.StatusCode, http.StatusMethodNotAllowed, uiBody(t, response))
		}
	}
}

func TestUIHidesSecrets(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		cfg.Providers["provider"].APIKey = "super-secret-provider-key"
		cfg.Providers["provider"].Headers = map[string]string{"x-secret": "header-secret-value"}
		cfg.Providers["provider"].BaseURL = "http://user:pass@upstream.internal/v1?token=query-secret"
		cfg.Providers["provider"].Endpoints[wireChatCompletions] = ProviderEndpoint{
			Path: "/chat/completions",
		}
	})
	client := uiLogin(t, server)

	var bodies strings.Builder
	for _, path := range []string{"/api/ui/session", "/api/ui/overview", "/api/ui/models", "/api/ui/providers"} {
		response := uiCall(t, client, http.MethodGet, server.URL+path, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
		bodies.WriteString(uiBody(t, response))
	}
	payload := bodies.String()
	for _, secret := range []string{
		"super-secret-provider-key",
		"header-secret-value",
		"query-secret",
		"user:pass",
		"key-a",
	} {
		if strings.Contains(payload, secret) {
			t.Fatalf("UI payload leaked %q: %s", secret, payload)
		}
	}
	if !strings.Contains(payload, "http://upstream.internal/v1") {
		t.Fatalf("sanitized base URL missing: %s", payload)
	}
}

func TestUIRequestHistoryShowsAllUsers(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)

	call := func(key, model string) {
		response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", key,
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", model, response.StatusCode)
		}
	}
	call("key-a", "model/a")
	call("key-b", "model-b")

	client := uiLogin(t, server)
	response := uiCall(t, client, http.MethodGet, server.URL+"/api/ui/requests", "", nil)
	requests := uiDecode(t, response)
	if requests["total"] != float64(2) {
		t.Fatalf("requests = %v", requests)
	}
	records := requests["records"].([]any)
	newest := records[0].(map[string]any)
	if newest["user"] != "bob" || newest["model"] != "model-b" || newest["status"] != float64(200) {
		t.Fatalf("newest record = %v", newest)
	}
	if newest["request_id"] == "" {
		t.Fatalf("empty request id: %v", newest)
	}
	oldest := records[1].(map[string]any)
	if oldest["user"] != "alice" || oldest["model"] != "model/a" {
		t.Fatalf("oldest record = %v", oldest)
	}

	// Health and index probes must not pollute the model request history.
	doJSON(t, http.MethodGet, server.URL+"/api/health", "", "")
	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/overview", "", nil)
	summary := uiDecode(t, response)["summary"].(map[string]any)
	if summary["requests"] != float64(2) {
		t.Fatalf("summary = %v", summary)
	}
}

func TestMemberOverviewAndRequestsAreScoped(t *testing.T) {
	t.Parallel()

	readerHash, err := core.HashPassword("reader-password")
	if err != nil {
		t.Fatalf("hash reader password: %v", err)
	}
	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		reader := &User{
			ID:            "reader",
			Name:          "Reader",
			AllowedModels: []string{"model/a"},
			PasswordHash:  readerHash,
		}
		cfg.UsersByKey["key-reader"] = reader
		cfg.UsersByID["reader"] = reader
	})

	call := func(key, model string) {
		response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", key,
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", model, response.StatusCode)
		}
	}
	call("key-a", "model/a")
	call("key-b", "model-b")
	call("key-reader", "model/a")

	reader := uiLoginWithPassword(t, server, "reader", "reader-password")

	overview := uiDecode(t, uiCall(t, reader, http.MethodGet, server.URL+"/api/ui/overview", "", nil))
	if _, leaked := overview["by_provider"]; leaked {
		t.Fatalf("member overview exposed provider breakdown: %v", overview)
	}
	counts, _ := overview["counts"].(map[string]any)
	if _, leaked := counts["providers"]; leaked {
		t.Fatalf("member overview exposed provider count: %v", counts)
	}
	if counts["models"] != float64(1) {
		t.Fatalf("member model count = %v", counts["models"])
	}
	summary, _ := overview["summary"].(map[string]any)
	if summary["requests"] != float64(1) {
		t.Fatalf("member summary = %v", summary)
	}
	recent, _ := overview["recent"].([]any)
	if len(recent) != 1 {
		t.Fatalf("member recent = %v", recent)
	}
	overviewRecord, _ := recent[0].(map[string]any)
	if overviewRecord["model"] != "model/a" || overviewRecord["user"] != "reader" {
		t.Fatalf("member overview record = %v", overviewRecord)
	}
	for _, key := range []string{"provider", "provider_name", "upstream_model"} {
		if _, leaked := overviewRecord[key]; leaked {
			t.Fatalf("member overview exposed %s: %v", key, overviewRecord)
		}
	}

	requests := uiDecode(t, uiCall(t, reader, http.MethodGet, server.URL+"/api/ui/requests", "", nil))
	if requests["total"] != float64(1) {
		t.Fatalf("member requests = %v", requests)
	}
	records, _ := requests["records"].([]any)
	record, _ := records[0].(map[string]any)
	if record["user"] != "reader" || record["model"] != "model/a" {
		t.Fatalf("member request record = %v", record)
	}
	for _, key := range []string{"provider", "provider_name", "upstream_model"} {
		if _, leaked := record[key]; leaked {
			t.Fatalf("member requests exposed %s: %v", key, record)
		}
	}
}

func TestUIDisplaysNamesInsteadOfIDs(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		cfg.Providers["provider"].Name = "Upstream Provider"
		cfg.Models["model/a"].Name = "Model A"
		cfg.UsersByKey["key-a"].Name = "Alice"
	})

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("request status = %d", response.StatusCode)
	}

	client := uiLogin(t, server)

	session := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/session", "", nil))
	user := session["user"].(map[string]any)
	if user["id"] != testAdminUsername || user["name"] != "Administrator" {
		t.Fatalf("session user = %v", user)
	}

	models := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/models", "", nil))["models"].([]any)
	var model map[string]any
	for _, item := range models {
		candidate := item.(map[string]any)
		if candidate["id"] == "model/a" {
			model = candidate
		}
	}
	if model == nil || model["name"] != "Model A" {
		t.Fatalf("model name = %v", model)
	}
	binding := model["providers"].([]any)[0].(map[string]any)
	if binding["name"] != "Upstream Provider" {
		t.Fatalf("binding name = %v", binding)
	}

	providers := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/providers", "", nil))["providers"].([]any)
	provider := providers[0].(map[string]any)
	providerModelNames, _ := provider["model_names"].(map[string]any)
	if provider["name"] != "Upstream Provider" || providerModelNames["model/a"] != "Model A" {
		t.Fatalf("provider = %v", provider)
	}

	records := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/requests", "", nil))["records"].([]any)
	record := records[0].(map[string]any)
	if record["model"] != "model/a" || record["model_name"] != "Model A" ||
		record["provider"] != "provider" || record["provider_name"] != "Upstream Provider" {
		t.Fatalf("record = %v", record)
	}
}

func TestUIUsageFromPassthroughJSON(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, payload map[string]any, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireChatCompletions,
				Status:        http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"chatcmpl-1","model":"model-a","choices":[],` +
						`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)),
			}, nil
		},
	}
	server := newTestServer(t, router, nil)

	response := doJSON(t, http.MethodPost, server.URL+"/api/v1/chat/completions", "key-a",
		`{"model":"model/a","messages":[{"role":"user","content":"hi"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d", response.StatusCode)
	}

	client := uiLogin(t, server)
	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/overview", "", nil)
	overview := uiDecode(t, response)
	summary := overview["summary"].(map[string]any)
	if summary["total_tokens"] != float64(18) || summary["input_tokens"] != float64(11) || summary["output_tokens"] != float64(7) {
		t.Fatalf("summary = %v", summary)
	}
	series := overview["series"].([]any)
	if len(series) != 24 {
		t.Fatalf("series length = %d", len(series))
	}
}

func TestUIDisabledWithoutUsers(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		cfg.UsersByKey = map[string]*User{}
		cfg.UsersByID = map[string]*User{}
	})
	client := uiClient(t, server)

	response := uiCall(t, client, http.MethodGet, server.URL+"/api/ui/session", "", nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("session status = %d", response.StatusCode)
	}
	body := uiDecode(t, response)
	detail, _ := body["error"].(map[string]any)
	if detail["code"] != "ui_disabled" {
		t.Fatalf("session error = %v", body)
	}

	response = uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login",
		`{"username":"admin","password":"anything"}`, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("login status = %d", response.StatusCode)
	}
}

func TestUISessionCookieSecureBehindTLSProxy(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)
	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login",
		`{"username":"`+testAdminUsername+`","password":"`+testAdminPassword+`"}`,
		map[string]string{"X-Forwarded-Proto": "https, http"})
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("cookies = %+v", cookies)
	}
}

// Administrator and member console flows. The fixtures above boot a static
// snapshot; these tests need a real store, so they use newAdminTestServer.

func TestAdminLoginAndCRUD(t *testing.T) {
	t.Parallel()

	server, app, password := newAdminTestServer(t)
	client := uiLoginWithPassword(t, server, testAdminUsername, password)

	session := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/session", "", nil))
	user, _ := session["user"].(map[string]any)
	if user["id"] != testAdminUsername || user["is_admin"] != true {
		t.Fatalf("session user = %v", user)
	}

	providers := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/providers", "", nil))
	if providers["providers"] == nil {
		t.Fatalf("providers response = %v", providers)
	}

	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/providers",
		`{"id":"p1","name":"Provider One","base_url":"https://api.example.com/v1","endpoints":{"chat_completions":{"path":"/v1/messages","converter":"openai_chat_to_claude_messages"}},"api_key":"sk-secret"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save provider status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	// A provider must be listed before any model binds to it, otherwise the
	// console cannot show or edit a provider it just created.
	providers = uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/providers", "", nil))
	listed, _ := providers["providers"].([]any)
	found := false
	for _, item := range listed {
		if record, _ := item.(map[string]any); record["id"] == "p1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unbound provider missing from list: %v", providers)
	}

	response = uiCall(t, client, http.MethodPost, server.URL+"/api/ui/models",
		`{"id":"m1","name":"Model One","providers":[{"provider_id":"p1","upstream_model":"upstream-model"}]}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save model status = %d: %s", response.StatusCode, uiBody(t, response))
	}

	response = uiCall(t, client, http.MethodPost, server.URL+"/api/ui/users",
		`{"id":"alice","name":"Alice","password":"alice-password","allowed_models":["m1"]}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save user status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	userPayload := uiDecode(t, response)
	savedUser, _ := userPayload["user"].(map[string]any)
	if savedUser["id"] != "alice" || savedUser["is_admin"] != false || savedUser["key_count"] != float64(0) {
		t.Fatalf("saved user = %v", userPayload)
	}
	if _, leaked := savedUser["password"]; leaked {
		t.Fatalf("password leaked in user payload: %v", savedUser)
	}

	cfg := app.Config()
	if cfg.Providers["p1"] == nil || cfg.Models["m1"] == nil || cfg.UsersByID["alice"] == nil {
		t.Fatalf("reload did not pick up changes: %+v", cfg)
	}
	if cfg.Providers["p1"].APIKey != "sk-secret" {
		t.Fatalf("provider key = %q", cfg.Providers["p1"].APIKey)
	}
	if cfg.Providers["p1"].BaseURL != "https://api.example.com/v1" {
		t.Fatalf("provider base URL = %q", cfg.Providers["p1"].BaseURL)
	}
	if endpoint := cfg.Providers["p1"].Endpoints[wireChatCompletions]; endpoint.ConverterID() != convert.OpenAIChatToClaudeMessages {
		t.Fatalf("chat converter = %q", endpoint.ConverterID())
	}
	if !cfg.UsersByID["alice"].CanUse("m1") {
		t.Fatal("user allowed models were not reloaded")
	}

	converters := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/converters", "", nil))
	definitions, _ := converters["converters"].([]any)
	if len(definitions) != 6 {
		t.Fatalf("converters = %v", converters["converters"])
	}
	formats, _ := converters["formats"].([]any)
	if len(formats) != 3 {
		t.Fatalf("formats = %v", converters["formats"])
	}

	// Deleting a bound provider must be refused; delete the model first.
	if response := uiCall(t, client, http.MethodDelete, server.URL+"/api/ui/providers/p1", "", nil); response.StatusCode == http.StatusOK {
		t.Fatal("provider bound to a model was deleted")
	}
	for _, path := range []string{"/api/ui/models/m1", "/api/ui/providers/p1", "/api/ui/users/alice"} {
		response := uiCall(t, client, http.MethodDelete, server.URL+path, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("DELETE %s status = %d: %s", path, response.StatusCode, uiBody(t, response))
		}
	}
	if cfg := app.Config(); len(cfg.Providers) != 0 || len(cfg.Models) != 0 {
		t.Fatalf("deleted records still loaded: %+v", cfg)
	}
}

func TestAdminProviderCatalogAndModelCapabilities(t *testing.T) {
	t.Parallel()

	server, _, password := newAdminTestServer(t)
	client := uiLoginWithPassword(t, server, testAdminUsername, password)

	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/providers",
		`{"id":"p1","name":"Provider One","base_url":"https://api.example.com/v1","upstream_models":["gpt-4o","gpt-4o-mini"],"endpoints":{"chat_completions":{"converter":"none"}}}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save provider status = %d: %s", response.StatusCode, uiBody(t, response))
	}

	providers := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/providers", "", nil))
	listed, _ := providers["providers"].([]any)
	if len(listed) != 1 {
		t.Fatalf("providers = %v", providers)
	}
	record, _ := listed[0].(map[string]any)
	upstream, _ := record["upstream_models"].([]any)
	if len(upstream) != 2 || upstream[0] != "gpt-4o" {
		t.Fatalf("upstream_models = %v", record["upstream_models"])
	}

	response = uiCall(t, client, http.MethodPost, server.URL+"/api/ui/models",
		`{"id":"alias","name":"Alias","reasoning":true,"tools":true,"providers":[{"provider_id":"p1","upstream_model":"gpt-4o"}]}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save model status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	model := uiDecode(t, uiCall(t, client, http.MethodGet, server.URL+"/api/ui/models/alias", "", nil))["model"].(map[string]any)
	if model["reasoning"] != true || model["tools"] != true {
		t.Fatalf("model capabilities = %v", model)
	}

	response = uiCall(t, client, http.MethodPost, server.URL+"/api/ui/models",
		`{"id":"bad","providers":[{"provider_id":"p1","upstream_model":"unknown-model"}]}`, nil)
	if response.StatusCode == http.StatusOK {
		t.Fatalf("model outside the provider catalog was accepted: %s", uiBody(t, response))
	}
}

func TestAPIKeyCannotLogInToConsole(t *testing.T) {
	t.Parallel()

	server, _, password := newAdminTestServer(t)
	admin := uiLoginWithPassword(t, server, testAdminUsername, password)

	response := uiCall(t, admin, http.MethodPost, server.URL+"/api/ui/keys", `{"name":"Login test"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create key status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	apiKey, _ := uiDecode(t, response)["key"].(map[string]any)["key"].(string)
	if apiKey == "" {
		t.Fatal("generated API key is empty")
	}

	client := uiClient(t, server)
	login := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login",
		fmt.Sprintf(`{"api_key":%q}`, apiKey), nil)
	if login.StatusCode != http.StatusBadRequest {
		t.Fatalf("API key login status = %d, want 400: %s", login.StatusCode, uiBody(t, login))
	}
	detail, _ := uiDecode(t, login)["error"].(map[string]any)
	if detail["code"] != "missing_credentials" {
		t.Fatalf("API key login error = %v", detail)
	}
	if cookies := login.Cookies(); len(cookies) != 0 {
		t.Fatalf("API key login set cookies: %v", cookies)
	}
	if response := uiCall(t, client, http.MethodGet, server.URL+"/api/ui/session", "", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after API key login = %d", response.StatusCode)
	}
}

func TestNormalUserOwnsKeysAndCannotAdminister(t *testing.T) {
	t.Parallel()

	server, app, password := newAdminTestServer(t)
	admin := uiLoginWithPassword(t, server, testAdminUsername, password)

	if response := uiCall(t, admin, http.MethodPost, server.URL+"/api/ui/users",
		`{"id":"reader","name":"Reader","password":"reader-password","allowed_models":["*"]}`, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("save user status = %d: %s", response.StatusCode, uiBody(t, response))
	}

	reader := uiLoginWithPassword(t, server, "reader", "reader-password")
	session := uiDecode(t, uiCall(t, reader, http.MethodGet, server.URL+"/api/ui/session", "", nil))
	user, _ := session["user"].(map[string]any)
	if user["id"] != "reader" || user["is_admin"] != false {
		t.Fatalf("normal user session = %v", session)
	}

	// Overview, the member Models page, and request history are available to
	// every account.
	for _, path := range []string{"/api/ui/overview", "/api/ui/catalog", "/api/ui/requests"} {
		if response := uiCall(t, reader, http.MethodGet, server.URL+path, "", nil); response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200: %s", path, response.StatusCode, uiBody(t, response))
		}
	}

	// Administration endpoints stay administrator-only.
	for _, path := range []string{"/api/ui/models", "/api/ui/providers", "/api/ui/users", "/api/ui/converters"} {
		if response := uiCall(t, reader, http.MethodGet, server.URL+path, "", nil); response.StatusCode != http.StatusForbidden {
			t.Fatalf("GET %s status = %d, want 403", path, response.StatusCode)
		}
	}
	if response := uiCall(t, reader, http.MethodPost, server.URL+"/api/ui/providers",
		`{"id":"p1","name":"Provider One","base_url":"https://api.example.com/v1","endpoints":{"chat_completions":{"converter":"none"}}}`, nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("normal user created a provider: %d", response.StatusCode)
	}

	// A normal user creates, lists, and deletes only their own keys.
	response := uiCall(t, reader, http.MethodPost, server.URL+"/api/ui/keys", `{"name":"Laptop"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reader create key status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	created := uiDecode(t, response)["key"].(map[string]any)
	keyID, _ := created["id"].(string)
	keyValue, _ := created["key"].(string)
	if keyID == "" || keyValue == "" || created["user_id"] != "reader" {
		t.Fatalf("created key = %v", created)
	}
	if cfg := app.Config(); cfg.UsersByKey[keyValue] == nil {
		t.Fatal("new key was not loaded into the gateway configuration")
	}

	listed := uiDecode(t, uiCall(t, reader, http.MethodGet, server.URL+"/api/ui/keys", "", nil))
	if keys, _ := listed["keys"].([]any); len(keys) != 1 {
		t.Fatalf("reader keys = %v", listed)
	}

	// The administrator cannot see or delete another account's keys.
	adminList := uiDecode(t, uiCall(t, admin, http.MethodGet, server.URL+"/api/ui/keys", "", nil))
	if keys, _ := adminList["keys"].([]any); len(keys) != 0 {
		t.Fatalf("admin saw another account's keys: %v", adminList)
	}
	if response := uiCall(t, admin, http.MethodDelete, server.URL+"/api/ui/keys/"+keyID, "", nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("admin deleted another account's key: %d", response.StatusCode)
	}

	if response := uiCall(t, reader, http.MethodDelete, server.URL+"/api/ui/keys/"+keyID, "", nil); response.StatusCode != http.StatusOK {
		t.Fatalf("reader delete key status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	if cfg := app.Config(); cfg.UsersByKey[keyValue] != nil {
		t.Fatal("deleted key still authenticates")
	}

	// A normal user can change their own password.
	if response := uiCall(t, reader, http.MethodPost, server.URL+"/api/ui/password",
		`{"current_password":"reader-password","new_password":"reader-password-2"}`, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("reader password change status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	uiLoginWithPassword(t, server, "reader", "reader-password-2")

	// Deleting the account cascades its keys.
	if response := uiCall(t, admin, http.MethodDelete, server.URL+"/api/ui/users/reader", "", nil); response.StatusCode != http.StatusOK {
		t.Fatalf("delete user status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	if cfg := app.Config(); cfg.UsersByID["reader"] != nil {
		t.Fatal("deleted user still loaded")
	}
}

func TestMemberModelCatalogHonorsAllowList(t *testing.T) {
	t.Parallel()

	server, _, password := newAdminTestServer(t)
	admin := uiLoginWithPassword(t, server, testAdminUsername, password)

	if response := uiCall(t, admin, http.MethodPost, server.URL+"/api/ui/providers",
		`{"id":"p1","name":"Provider One","base_url":"https://api.example.com/v1","upstream_models":["up1","up2","up3"],"endpoints":{"chat_completions":{"converter":"none"}}}`, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("save provider status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	for _, model := range []struct{ id, upstream string }{{"m1", "up1"}, {"m2", "up2"}} {
		body := fmt.Sprintf(`{"id":%q,"name":%q,"providers":[{"provider_id":"p1","upstream_model":%q}]}`, model.id, strings.ToUpper(model.id), model.upstream)
		if response := uiCall(t, admin, http.MethodPost, server.URL+"/api/ui/models", body, nil); response.StatusCode != http.StatusOK {
			t.Fatalf("save model %s status = %d: %s", model.id, response.StatusCode, uiBody(t, response))
		}
	}
	if response := uiCall(t, admin, http.MethodPost, server.URL+"/api/ui/users",
		`{"id":"reader","name":"Reader","password":"reader-password","allowed_models":["m1"]}`, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("save user status = %d: %s", response.StatusCode, uiBody(t, response))
	}

	reader := uiLoginWithPassword(t, server, "reader", "reader-password")
	catalog := uiDecode(t, uiCall(t, reader, http.MethodGet, server.URL+"/api/ui/catalog", "", nil))
	listed := catalog["models"].([]any)
	if len(listed) != 1 {
		t.Fatalf("reader catalog = %v", catalog)
	}
	model := listed[0].(map[string]any)
	if model["id"] != "m1" || model["name"] != "M1" {
		t.Fatalf("reader model = %v", model)
	}
	if _, leaked := model["providers"].([]any); leaked {
		t.Fatalf("catalog exposed provider bindings: %v", model)
	}

	// The wildcard saved by the console's "Select all" control also unlocks
	// models that are created after the user was saved.
	if response := uiCall(t, admin, http.MethodPut, server.URL+"/api/ui/users/reader",
		`{"name":"Reader","allowed_models":["*"]}`, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("update user status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	if response := uiCall(t, admin, http.MethodPost, server.URL+"/api/ui/models",
		`{"id":"m3","name":"M3","providers":[{"provider_id":"p1","upstream_model":"up3"}]}`, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("save future model status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	catalog = uiDecode(t, uiCall(t, reader, http.MethodGet, server.URL+"/api/ui/catalog", "", nil))
	if listed := catalog["models"].([]any); len(listed) != 3 {
		t.Fatalf("wildcard catalog = %v", catalog)
	}
}

func TestAdminPasswordChange(t *testing.T) {
	t.Parallel()

	server, _, password := newAdminTestServer(t)
	client := uiLoginWithPassword(t, server, testAdminUsername, password)

	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/password",
		`{"current_password":"wrong","new_password":"new-password-123"}`, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current password status = %d", response.StatusCode)
	}

	response = uiCall(t, client, http.MethodPost, server.URL+"/api/ui/password",
		`{"current_password":"`+password+`","new_password":"short"}`, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("short password status = %d", response.StatusCode)
	}

	response = uiCall(t, client, http.MethodPost, server.URL+"/api/ui/password",
		`{"current_password":"`+password+`","new_password":"new-password-123"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("password change status = %d: %s", response.StatusCode, uiBody(t, response))
	}

	// The current session survives; new logins need the new password.
	if response := uiCall(t, client, http.MethodGet, server.URL+"/api/ui/session", "", nil); response.StatusCode != http.StatusOK {
		t.Fatalf("session after password change = %d", response.StatusCode)
	}

	oldClient := uiClient(t, server)
	oldLogin := uiCall(t, oldClient, http.MethodPost, server.URL+"/api/ui/login",
		fmt.Sprintf(`{"username":%q,"password":%q}`, testAdminUsername, password), nil)
	if oldLogin.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password login status = %d", oldLogin.StatusCode)
	}

	newClient := uiLoginWithPassword(t, server, testAdminUsername, "new-password-123")
	if response := uiCall(t, newClient, http.MethodGet, server.URL+"/api/ui/users", "", nil); response.StatusCode != http.StatusOK {
		t.Fatalf("new password session status = %d", response.StatusCode)
	}
}

func TestAdminLoginRejectsBadCredentials(t *testing.T) {
	t.Parallel()

	server, _, _ := newAdminTestServer(t)
	client := uiClient(t, server)

	for _, body := range []string{
		`{"username":"admin","password":"wrong"}`,
		`{"username":"nobody","password":"whatever"}`,
		`{"username":"admin","password":""}`,
	} {
		response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login", body, nil)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status = %d", body, response.StatusCode)
		}
		detail, _ := uiDecode(t, response)["error"].(map[string]any)
		if detail["code"] != "invalid_credentials" {
			t.Fatalf("%s error = %v", body, detail)
		}
	}
}
