package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

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
