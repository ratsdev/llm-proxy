package llmproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ratsdev/llm-proxy/ir"
)

func uiClient(t *testing.T, _ *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

func uiCall(t *testing.T, client *http.Client, method, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		request.Header.Set("content-type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func uiBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}

func uiLogin(t *testing.T, server *httptest.Server, key string) *http.Client {
	t.Helper()
	client := uiClient(t, server)
	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login",
		`{"api_key":"`+key+`"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	return client
}

func uiDecode(t *testing.T, response *http.Response) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(uiBody(t, response)), &object); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return object
}

func TestUIPageAndAssets(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)

	for _, path := range []string{"/ui", "/ui/"} {
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
		if !strings.Contains(uiBody(t, response), "/ui/assets/app.js") {
			t.Fatalf("%s does not load the app bundle", path)
		}
	}

	assets := map[string]string{
		"/ui/assets/app.js":     "javascript",
		"/ui/assets/styles.css": "text/css",
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

	missing := uiCall(t, client, http.MethodGet, server.URL+"/ui/assets/missing.js", "", nil)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset status = %d", missing.StatusCode)
	}

	index := uiCall(t, client, http.MethodGet, server.URL+"/api", "", nil)
	if body := uiDecode(t, index); body["ui"] != "/ui" {
		t.Fatalf("index ui = %v", body["ui"])
	}
}

func TestUIAssetRevalidation(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)

	response := uiCall(t, client, http.MethodGet, server.URL+"/ui/assets/app.js", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("asset status = %d", response.StatusCode)
	}
	etag := response.Header.Get("etag")
	if etag == "" {
		t.Fatal("asset response is missing an etag")
	}
	_ = uiBody(t, response)

	cached := uiCall(t, client, http.MethodGet, server.URL+"/ui/assets/app.js", "", map[string]string{
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

func TestUILoginSessionAndACL(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)
	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login", `{"api_key":"key-a"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != uiSessionCookie || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", cookie)
	}
	if cookie.Path != "/api/ui" {
		t.Fatalf("cookie path = %q", cookie.Path)
	}
	login := uiDecode(t, response)
	user, _ := login["user"].(map[string]any)
	if user["id"] != "alice" || user["unrestricted"] != false {
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
	if sessionUser["id"] != "alice" {
		t.Fatalf("session user = %v", session)
	}

	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/models", "", nil)
	models := uiDecode(t, response)["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("alice sees %d models, want 1", len(models))
	}
	if models[0].(map[string]any)["id"] != "model/a" {
		t.Fatalf("alice model = %v", models[0])
	}

	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/providers", "", nil)
	providers := uiDecode(t, response)["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("alice sees %d providers, want 1", len(providers))
	}
	provider := providers[0].(map[string]any)
	if provider["id"] != "provider" {
		t.Fatalf("provider = %v", provider)
	}
	if provider["timeout"] != float64(600) {
		t.Fatalf("provider timeout = %v, want the 600s default", provider["timeout"])
	}
	if models := provider["models"].([]any); len(models) != 1 || models[0] != "model/a" {
		t.Fatalf("provider models = %v", provider["models"])
	}

	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/overview", "", nil)
	overview := uiDecode(t, response)
	counts, _ := overview["counts"].(map[string]any)
	if counts["models"] != float64(1) || counts["providers"] != float64(1) {
		t.Fatalf("overview counts = %v", counts)
	}
	gateway, _ := overview["gateway"].(map[string]any)
	if gateway["version"] != Version || gateway["read_only"] != true {
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
		{"wrong key", `{"api_key":"nope"}`, http.StatusUnauthorized, "invalid_api_key"},
		{"missing key", `{}`, http.StatusBadRequest, "missing_api_key"},
		{"blank key", `{"api_key":"   "}`, http.StatusBadRequest, "missing_api_key"},
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
	client := uiLogin(t, server, "key-a")

	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/logout", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", response.StatusCode)
	}
	response = uiCall(t, client, http.MethodGet, server.URL+"/api/ui/session", "", nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d", response.StatusCode)
	}
}

func TestUIReadOnly(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiLogin(t, server, "key-a")

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/ui/models"},
		{http.MethodPut, "/api/ui/providers"},
		{http.MethodDelete, "/api/ui/requests"},
		{http.MethodGet, "/api/ui/login"},
		{http.MethodDelete, "/api/ui/overview"},
		{http.MethodGet, "/api/ui/config"},
	}
	for _, testCase := range cases {
		response := uiCall(t, client, testCase.method, server.URL+testCase.path, "{}", nil)
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d", testCase.method, testCase.path, response.StatusCode)
		}
	}
}

func TestUIHidesSecrets(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		cfg.Providers["provider"].Key = "super-secret-provider-key"
		cfg.Providers["provider"].Headers = map[string]string{"x-secret": "header-secret-value"}
		cfg.Providers["provider"].BaseURL = "http://user:pass@upstream.internal/v1?token=query-secret"
	})
	client := uiLogin(t, server, "key-a")

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

func TestUIRequestHistoryIsPerUser(t *testing.T) {
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

	alice := uiLogin(t, server, "key-a")
	response := uiCall(t, alice, http.MethodGet, server.URL+"/api/ui/requests", "", nil)
	requests := uiDecode(t, response)
	if requests["total"] != float64(1) {
		t.Fatalf("alice requests = %v", requests)
	}
	records := requests["records"].([]any)
	record := records[0].(map[string]any)
	if record["user"] != "alice" || record["model"] != "model/a" || record["status"] != float64(200) {
		t.Fatalf("alice record = %v", record)
	}
	if record["request_id"] == "" {
		t.Fatalf("empty request id: %v", record)
	}

	bob := uiLogin(t, server, "key-b")
	response = uiCall(t, bob, http.MethodGet, server.URL+"/api/ui/requests", "", nil)
	requests = uiDecode(t, response)
	records = requests["records"].([]any)
	if len(records) != 1 || records[0].(map[string]any)["model"] != "model-b" {
		t.Fatalf("bob requests = %v", requests)
	}

	// Health and index probes must not pollute the model request history.
	doJSON(t, http.MethodGet, server.URL+"/api/health", "", "")
	response = uiCall(t, bob, http.MethodGet, server.URL+"/api/ui/overview", "", nil)
	summary := uiDecode(t, response)["summary"].(map[string]any)
	if summary["requests"] != float64(1) {
		t.Fatalf("bob summary = %v", summary)
	}
}

func TestUIUsageFromPassthroughJSON(t *testing.T) {
	t.Parallel()

	router := &fakeRouter{
		send: func(model *Model, operation string, request *ir.Request, stickyID, inboundProtocol string) (*RouteResult, error) {
			return &RouteResult{
				ProviderID:    "provider",
				UpstreamModel: "model-a",
				WireFormat:    wireOpenAI,
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

	client := uiLogin(t, server, "key-a")
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

func TestUISessionExpiry(t *testing.T) {
	t.Parallel()

	store := newUIState()
	now := time.Now()
	token, expires := store.createSession("alice", now)
	if _, ok := store.lookupSession(token, now.Add(time.Minute)); !ok {
		t.Fatal("session missing before expiry")
	}
	if _, ok := store.lookupSession(token, expires.Add(time.Second)); ok {
		t.Fatal("session still valid after expiry")
	}
	if _, ok := store.lookupSession(token, now); ok {
		t.Fatal("expired session was not pruned")
	}
}

func TestUIHistorySnapshot(t *testing.T) {
	t.Parallel()

	t.Run("newest first", func(t *testing.T) {
		store := newUIState()
		for index := 0; index < 3; index++ {
			store.recordRequest(uiRequestRecord{User: "alice", RequestID: fmt.Sprint(index)})
		}

		if store.historyFull() {
			t.Fatal("partial history was reported as full")
		}
		records := store.snapshotRecordsFor("alice")
		if len(records) != 3 {
			t.Fatalf("history length = %d, want 3", len(records))
		}
		if records[0].RequestID != "2" || records[2].RequestID != "0" {
			t.Fatalf("history order = %v", []string{records[0].RequestID, records[1].RequestID, records[2].RequestID})
		}
	})

	t.Run("ring buffer", func(t *testing.T) {
		store := newUIState()
		for index := 0; index < uiHistorySize+3; index++ {
			store.recordRequest(uiRequestRecord{User: "alice", RequestID: fmt.Sprint(index)})
		}

		if !store.historyFull() {
			t.Fatal("full history was not reported as full")
		}
		records := store.snapshotRecordsFor("alice")
		if len(records) != uiHistorySize {
			t.Fatalf("history length = %d, want %d", len(records), uiHistorySize)
		}
		if records[0].RequestID != fmt.Sprint(uiHistorySize+2) {
			t.Fatalf("newest record = %q", records[0].RequestID)
		}
		if records[len(records)-1].RequestID != "3" {
			t.Fatalf("oldest record = %q", records[len(records)-1].RequestID)
		}
	})

	t.Run("filters by user", func(t *testing.T) {
		store := newUIState()
		store.recordRequest(uiRequestRecord{User: "alice", RequestID: "a1"})
		store.recordRequest(uiRequestRecord{User: "bob", RequestID: "b1"})
		store.recordRequest(uiRequestRecord{User: "alice", RequestID: "a2"})

		records := store.snapshotRecordsFor("alice")
		if len(records) != 2 || records[0].RequestID != "a2" || records[1].RequestID != "a1" {
			t.Fatalf("alice history = %v", records)
		}
	})
}

func TestUIDisabledWithoutUsers(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, func(cfg *Config) {
		cfg.UsersByKey = map[string]*User{}
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

	response = uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login", `{"api_key":"anything"}`, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("login status = %d", response.StatusCode)
	}
}

func TestUISessionCookieSecureBehindTLSProxy(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeRouter{}, nil)
	client := uiClient(t, server)
	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login", `{"api_key":"key-a"}`,
		map[string]string{"X-Forwarded-Proto": "https, http"})
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("cookies = %+v", cookies)
	}
}
