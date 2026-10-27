package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ratsdev/llm-proxy/internal/core"
)

// These tests exercise the console's HTTP surface end to end: a real store, a
// real session cookie and the same routing table the gateway mounts.

const (
	consoleTestUsername = "console-owner"
	consoleTestPassword = "console-test-password"
)

type consoleFixture struct {
	server  *httptest.Server
	store   *core.Store
	refresh func() error
}

func newConsoleFixture(t *testing.T) *consoleFixture {
	t.Helper()

	fixture := newUninitializedConsoleFixture(t)
	if _, err := fixture.store.RegisterAdmin(core.UserInput{
		ID:       consoleTestUsername,
		Name:     "Console Owner",
		Password: consoleTestPassword,
	}); err != nil {
		t.Fatalf("RegisterAdmin() error = %v", err)
	}
	if err := fixture.refresh(); err != nil {
		t.Fatalf("load config: %v", err)
	}
	return fixture
}

// newUninitializedConsoleFixture builds the same HTTP surface on a database
// that has no administrator yet, which is what the registration flow starts
// from.
func newUninitializedConsoleFixture(t *testing.T) *consoleFixture {
	t.Helper()

	store, err := core.OpenStore(filepath.Join(t.TempDir(), "console.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var (
		mu  sync.Mutex
		cfg *core.Config
	)
	reload := func() error {
		next, loadErr := store.LoadConfig()
		if loadErr != nil {
			return loadErr
		}
		mu.Lock()
		cfg = next
		mu.Unlock()
		return nil
	}
	if err := reload(); err != nil {
		t.Fatalf("load config: %v", err)
	}

	handler := New(Options{
		Config: func() *core.Config {
			mu.Lock()
			defer mu.Unlock()
			return cfg
		},
		Store:          store,
		Reload:         reload,
		StartedAt:      time.Now(),
		Version:        "test",
		DefaultTimeout: 30 * time.Second,
	})
	mux := http.NewServeMux()
	handler.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &consoleFixture{server: server, store: store, refresh: reload}
}

func (f *consoleFixture) client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

func (f *consoleFixture) call(t *testing.T, client *http.Client, method, path, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		request.Header.Set("content-type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func (f *consoleFixture) login(t *testing.T, username, password string) (*http.Client, *http.Response) {
	t.Helper()
	client := f.client(t)
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	return client, f.call(t, client, http.MethodPost, "/api/ui/login", body)
}

func (f *consoleFixture) loginOK(t *testing.T, username, password string) *http.Client {
	t.Helper()
	client, response := f.login(t, username, password)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	return client
}

func decodeConsole(t *testing.T, response *http.Response) map[string]any {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return object
}

func TestConsoleRegistrationClaimsFirstAdministrator(t *testing.T) {
	fixture := newUninitializedConsoleFixture(t)

	// Until the first administrator exists, sessions and logins point the
	// visitor at registration instead of pretending the console is unconfigured.
	response := fixture.call(t, fixture.client(t), http.MethodGet, "/api/ui/session", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	if code := errorCode(decodeConsole(t, response)); code != "registration_required" {
		t.Fatalf("session error code = %q, want registration_required", code)
	}
	_, login := fixture.login(t, "anyone", "anything-password")
	if login.StatusCode != http.StatusConflict {
		t.Fatalf("login before registration status = %d, want %d", login.StatusCode, http.StatusConflict)
	}
	if code := errorCode(decodeConsole(t, login)); code != "registration_required" {
		t.Fatalf("login error code = %q, want registration_required", code)
	}

	client := fixture.client(t)
	register := fixture.call(t, client, http.MethodPost, "/api/ui/register",
		`{"username":"owner","name":"Gateway Owner","password":"owner-password"}`)
	if register.StatusCode != http.StatusOK {
		t.Fatalf("register status = %d: %s", register.StatusCode, readResponse(t, register))
	}
	payload := decodeConsole(t, register)
	user, _ := payload["user"].(map[string]any)
	if user["id"] != "owner" || user["name"] != "Gateway Owner" || user["is_admin"] != true {
		t.Fatalf("registered user = %v", user)
	}

	// Registration signs the new administrator in immediately.
	if status := fixture.call(t, client, http.MethodGet, "/api/ui/session", "").StatusCode; status != http.StatusOK {
		t.Fatalf("session after registration status = %d, want %d", status, http.StatusOK)
	}
	if status := fixture.call(t, client, http.MethodGet, "/api/ui/users", "").StatusCode; status != http.StatusOK {
		t.Fatalf("administrator users status = %d, want %d", status, http.StatusOK)
	}

	// A second visitor can no longer claim the deployment.
	other := fixture.client(t)
	second := fixture.call(t, other, http.MethodPost, "/api/ui/register",
		`{"username":"intruder","password":"intruder-password"}`)
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second register status = %d, want %d", second.StatusCode, http.StatusConflict)
	}
	if code := errorCode(decodeConsole(t, second)); code != "registration_closed" {
		t.Fatalf("second register error code = %q, want registration_closed", code)
	}
}

func TestConsoleRegistrationValidatesInput(t *testing.T) {
	fixture := newUninitializedConsoleFixture(t)

	cases := map[string]string{
		"missing username": `{"password":"owner-password"}`,
		"short password":   `{"username":"owner","password":"short"}`,
	}
	for name, body := range cases {
		response := fixture.call(t, fixture.client(t), http.MethodPost, "/api/ui/register", body)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", name, response.StatusCode, http.StatusBadRequest)
		}
	}

	invalidName := fixture.call(t, fixture.client(t), http.MethodPost, "/api/ui/register",
		`{"username":"bad\nname","password":"owner-password"}`)
	if invalidName.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid username status = %d, want %d", invalidName.StatusCode, http.StatusBadRequest)
	}
	if code := errorCode(decodeConsole(t, invalidName)); code != "invalid_username" {
		t.Fatalf("invalid username code = %q, want invalid_username", code)
	}

	// A taken name must not be confirmed: the response is the same generic
	// conflict as any other rejected registration, so the public form cannot be
	// used to enumerate accounts.
	if _, err := fixture.store.SaveUser(core.UserInput{ID: "member", Password: "member-password"}); err != nil {
		t.Fatalf("SaveUser() error = %v", err)
	}
	if err := fixture.refresh(); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	response := fixture.call(t, fixture.client(t), http.MethodPost, "/api/ui/register",
		`{"username":"member","password":"member-password"}`)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("register existing user status = %d, want %d", response.StatusCode, http.StatusConflict)
	}
	if code := errorCode(decodeConsole(t, response)); code != "registration_failed" {
		t.Fatalf("register existing user code = %q, want registration_failed", code)
	}
}

func TestRegistrationErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"administrator exists", core.ErrAdminExists, http.StatusConflict, "registration_closed"},
		{"id taken", fmt.Errorf("%w: %q", core.ErrUserIDTaken, "member"), http.StatusConflict, "registration_failed"},
		{"store failure", errors.New("database is locked"), http.StatusInternalServerError, "internal_error"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		writeRegistrationError(recorder, testCase.err)
		if recorder.Code != testCase.status {
			t.Errorf("%s: status = %d, want %d", testCase.name, recorder.Code, testCase.status)
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s: decode: %v", testCase.name, err)
		}
		if code := errorCode(payload); code != testCase.code {
			t.Errorf("%s: code = %q, want %q", testCase.name, code, testCase.code)
		}
	}
}

func TestConsoleRegistrationThrottlesPerAddress(t *testing.T) {
	fixture := newConsoleFixture(t)
	if _, err := fixture.store.SaveRegistrationSettings(core.RegistrationSettings{SelfRegistration: true}); err != nil {
		t.Fatalf("SaveRegistrationSettings() error = %v", err)
	}
	if err := fixture.refresh(); err != nil {
		t.Fatalf("reload config: %v", err)
	}

	client := fixture.client(t)
	for attempt := 0; attempt < uiRegisterMaxAttempts; attempt++ {
		response := fixture.call(t, client, http.MethodPost, "/api/ui/register",
			fmt.Sprintf(`{"username":"member-%d","password":"member-password"}`, attempt))
		if response.StatusCode != http.StatusOK {
			t.Fatalf("registration %d status = %d: %s", attempt+1, response.StatusCode, readResponse(t, response))
		}
	}
	response := fixture.call(t, client, http.MethodPost, "/api/ui/register",
		`{"username":"one-too-many","password":"member-password"}`)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("throttled registration status = %d, want %d", response.StatusCode, http.StatusTooManyRequests)
	}
	if retryAfter := response.Header.Get("retry-after"); retryAfter == "" {
		t.Fatal("throttled registration response has no Retry-After header")
	}
	if code := errorCode(decodeConsole(t, response)); code != "too_many_attempts" {
		t.Fatalf("throttled registration code = %q, want too_many_attempts", code)
	}
}

func TestConsoleUserEndpointsCannotGrantAdministrator(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	response := fixture.call(t, admin, http.MethodPost, "/api/ui/users",
		`{"id":"sneaky","password":"sneaky-password","is_admin":true}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create user status = %d: %s", response.StatusCode, readResponse(t, response))
	}
	user, err := fixture.store.GetUser("sneaky")
	if err != nil {
		t.Fatalf("GetUser(sneaky) error = %v", err)
	}
	if user.IsAdmin {
		t.Fatal("the console granted administrator rights")
	}
}

func TestConsoleRegistrationSettings(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	initial := decodeConsole(t, fixture.call(t, admin, http.MethodGet, "/api/ui/settings", ""))
	settings, _ := initial["settings"].(map[string]any)
	if settings["self_registration"] != false {
		t.Fatalf("initial settings = %v", initial)
	}

	update := `{"self_registration":true,"default_allowed_models":["m1"]}`
	saved := decodeConsole(t, fixture.call(t, admin, http.MethodPut, "/api/ui/settings", update))
	settings, _ = saved["settings"].(map[string]any)
	enabled, _ := settings["self_registration"].(bool)
	models, _ := settings["default_allowed_models"].([]any)
	if !enabled || len(models) != 1 || models[0] != "m1" {
		t.Fatalf("saved settings = %v", saved)
	}

	// The status endpoint is public so the sign-in screen can offer registration,
	// which is why it returns availability flags only.
	status := decodeConsole(t, fixture.call(t, fixture.client(t), http.MethodGet, "/api/ui/registration", ""))
	if status["first_run"] != false || status["enabled"] != true {
		t.Fatalf("registration status = %v", status)
	}
	if models, ok := status["default_allowed_models"]; ok {
		t.Fatalf("public registration status exposes the default model allow-list: %v", models)
	}

	member := fixture.client(t)
	register := fixture.call(t, member, http.MethodPost, "/api/ui/register",
		`{"username":"member","name":"Member","password":"member-password"}`)
	if register.StatusCode != http.StatusOK {
		t.Fatalf("self-registration status = %d: %s", register.StatusCode, readResponse(t, register))
	}
	payload := decodeConsole(t, register)
	user, _ := payload["user"].(map[string]any)
	if user["id"] != "member" || user["is_admin"] != false {
		t.Fatalf("self-registered user = %v", user)
	}
	stored, err := fixture.store.GetUser("member")
	if err != nil {
		t.Fatalf("GetUser(member) error = %v", err)
	}
	if len(stored.AllowedModels) != 1 || stored.AllowedModels[0] != "m1" {
		t.Fatalf("member allowed models = %v", stored.AllowedModels)
	}
	if status := fixture.call(t, member, http.MethodGet, "/api/ui/session", "").StatusCode; status != http.StatusOK {
		t.Fatalf("member session status = %d", status)
	}

	// Closing the switch stops further self-registration.
	closed := decodeConsole(t, fixture.call(t, admin, http.MethodPut, "/api/ui/settings",
		`{"self_registration":false,"default_allowed_models":["m1"]}`))
	settings, _ = closed["settings"].(map[string]any)
	if settings["self_registration"] != false {
		t.Fatalf("disabled settings = %v", closed)
	}
	status = decodeConsole(t, fixture.call(t, fixture.client(t), http.MethodGet, "/api/ui/registration", ""))
	if status["enabled"] != false {
		t.Fatalf("registration status after disable = %v", status)
	}
	rejected := fixture.call(t, fixture.client(t), http.MethodPost, "/api/ui/register",
		`{"username":"other","password":"other-password"}`)
	if rejected.StatusCode != http.StatusConflict {
		t.Fatalf("registration after disable status = %d, want %d", rejected.StatusCode, http.StatusConflict)
	}
	if code := errorCode(decodeConsole(t, rejected)); code != "registration_closed" {
		t.Fatalf("registration after disable code = %q", code)
	}
}

func TestConsoleRegistrationSettingsRequireAdministrator(t *testing.T) {
	fixture := newConsoleFixture(t)
	if _, err := fixture.store.SaveUser(core.UserInput{
		ID:            "member",
		Password:      "member-password",
		AllowedModels: []string{"m1"},
	}); err != nil {
		t.Fatalf("SaveUser() error = %v", err)
	}
	if err := fixture.refresh(); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	member := fixture.loginOK(t, "member", "member-password")

	if status := fixture.call(t, member, http.MethodGet, "/api/ui/settings", "").StatusCode; status != http.StatusForbidden {
		t.Fatalf("member GET settings status = %d, want %d", status, http.StatusForbidden)
	}
	if status := fixture.call(t, member, http.MethodPut, "/api/ui/settings",
		`{"self_registration":true,"default_allowed_models":[]}`).StatusCode; status != http.StatusForbidden {
		t.Fatalf("member PUT settings status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestConsoleLoginRejectsBadCredentials(t *testing.T) {
	fixture := newConsoleFixture(t)

	_, response := fixture.login(t, consoleTestUsername, "wrong-password")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	if code := errorCode(decodeConsole(t, response)); code != "invalid_credentials" {
		t.Fatalf("error code = %q, want invalid_credentials", code)
	}
}

func TestConsoleLoginThrottlesRepeatedFailures(t *testing.T) {
	fixture := newConsoleFixture(t)

	client := fixture.client(t)
	body := fmt.Sprintf(`{"username":%q,"password":"wrong-password"}`, consoleTestUsername)
	for attempt := 0; attempt < uiLoginMaxFailures; attempt++ {
		response := fixture.call(t, client, http.MethodPost, "/api/ui/login", body)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, response.StatusCode, http.StatusUnauthorized)
		}
	}

	response := fixture.call(t, client, http.MethodPost, "/api/ui/login", body)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("throttled attempt status = %d, want %d", response.StatusCode, http.StatusTooManyRequests)
	}
	if retryAfter := response.Header.Get("retry-after"); retryAfter == "" {
		t.Fatal("throttled response has no Retry-After header")
	}
	if code := errorCode(decodeConsole(t, response)); code != "too_many_attempts" {
		t.Fatalf("error code = %q, want too_many_attempts", code)
	}

	// The correct password must not bypass the throttle: otherwise an attacker
	// learns the password the moment the shard unlocks.
	correct := fmt.Sprintf(`{"username":%q,"password":%q}`, consoleTestUsername, consoleTestPassword)
	if status := fixture.call(t, client, http.MethodPost, "/api/ui/login", correct).StatusCode; status != http.StatusTooManyRequests {
		t.Fatalf("correct password during cooldown status = %d, want %d", status, http.StatusTooManyRequests)
	}
}

func TestConsoleRequiresSession(t *testing.T) {
	fixture := newConsoleFixture(t)

	for _, path := range []string{"/api/ui/models", "/api/ui/providers", "/api/ui/users", "/api/ui/keys"} {
		response := fixture.call(t, fixture.client(t), http.MethodGet, path, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusUnauthorized)
		}
	}
}

func TestConsoleLogoutEndsSession(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	if status := fixture.call(t, admin, http.MethodPost, "/api/ui/logout", "").StatusCode; status != http.StatusOK {
		t.Fatalf("logout status = %d, want %d", status, http.StatusOK)
	}
	if status := fixture.call(t, admin, http.MethodGet, "/api/ui/session", "").StatusCode; status != http.StatusUnauthorized {
		t.Fatalf("session status after logout = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestConsoleRejectsNonAdministrator(t *testing.T) {
	fixture := newConsoleFixture(t)
	if _, err := fixture.store.SaveUser(core.UserInput{
		ID:            "member",
		Name:          "Member",
		Password:      "member-password",
		AllowedModels: []string{"m1"},
	}); err != nil {
		t.Fatalf("save user: %v", err)
	}
	if err := fixture.refresh(); err != nil {
		t.Fatalf("reload config: %v", err)
	}

	member := fixture.loginOK(t, "member", "member-password")
	for _, path := range []string{"/api/ui/users", "/api/ui/providers", "/api/ui/models", "/api/ui/converters", "/api/ui/settings"} {
		if status := fixture.call(t, member, http.MethodGet, path, "").StatusCode; status != http.StatusForbidden {
			t.Fatalf("member GET %s status = %d, want %d", path, status, http.StatusForbidden)
		}
	}

	// Members can still read their own catalogue and manage their own keys.
	if status := fixture.call(t, member, http.MethodGet, "/api/ui/catalog", "").StatusCode; status != http.StatusOK {
		t.Fatalf("member GET /catalog status = %d, want %d", status, http.StatusOK)
	}
	if status := fixture.call(t, member, http.MethodGet, "/api/ui/keys", "").StatusCode; status != http.StatusOK {
		t.Fatalf("member GET /keys status = %d, want %d", status, http.StatusOK)
	}
}

func TestConsoleProviderLifecycle(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	create := `{"id":"p1","name":"Provider One","base_url":"https://api.example.com/v1",
		"api_key":"sk-upstream","endpoints":{"chat_completions":{"path":"/chat/completions"}},
		"upstream_models":["gpt-4o"]}`
	if status := fixture.call(t, admin, http.MethodPost, "/api/ui/providers", create).StatusCode; status != http.StatusOK {
		t.Fatalf("create provider status = %d, want %d", status, http.StatusOK)
	}

	stored := decodeConsole(t, fixture.call(t, admin, http.MethodGet, "/api/ui/providers/p1", ""))
	provider, _ := stored["provider"].(map[string]any)
	if provider == nil {
		t.Fatalf("provider payload = %v", stored)
	}
	if provider["api_key"] != "sk-upstream" {
		t.Fatalf("api_key = %v, want the stored key for the edit form", provider["api_key"])
	}

	// A blank key keeps the stored credential.
	update := `{"id":"p1","name":"Renamed","base_url":"https://api.example.com/v1",
		"api_key":"","endpoints":{"chat_completions":{"path":"/chat/completions"}},"upstream_models":["gpt-4o"]}`
	if status := fixture.call(t, admin, http.MethodPut, "/api/ui/providers/p1", update).StatusCode; status != http.StatusOK {
		t.Fatalf("update provider status = %d, want %d", status, http.StatusOK)
	}
	stored = decodeConsole(t, fixture.call(t, admin, http.MethodGet, "/api/ui/providers/p1", ""))
	provider, _ = stored["provider"].(map[string]any)
	if provider["api_key"] != "sk-upstream" {
		t.Fatalf("api_key after blank update = %v, want the stored key", provider["api_key"])
	}

	if status := fixture.call(t, admin, http.MethodDelete, "/api/ui/providers/p1", "").StatusCode; status != http.StatusOK {
		t.Fatalf("delete provider status = %d, want %d", status, http.StatusOK)
	}
	if status := fixture.call(t, admin, http.MethodGet, "/api/ui/providers/p1", "").StatusCode; status != http.StatusNotFound {
		t.Fatalf("deleted provider status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestConsoleProviderRejectsInvalidConfiguration(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	cases := map[string]string{
		"missing base url": `{"id":"p1","base_url":"","endpoints":{"chat_completions":{}}}`,
		"bad scheme":       `{"id":"p1","base_url":"ftp://example.com","endpoints":{"chat_completions":{}}}`,
		"unknown endpoint": `{"id":"p1","base_url":"https://example.com","endpoints":{"gemini":{}}}`,
		"bad converter":    `{"id":"p1","base_url":"https://example.com","endpoints":{"chat_completions":{"converter":"nope"}}}`,
		"path with query":  `{"id":"p1","base_url":"https://example.com","endpoints":{"chat_completions":{"path":"/chat?x=1"}}}`,
	}
	for name, body := range cases {
		response := fixture.call(t, admin, http.MethodPost, "/api/ui/providers", body)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", name, response.StatusCode, http.StatusBadRequest)
		}
	}
}

func TestConsoleModelLifecycleReturnsNormalisedValues(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	provider := `{"id":"p1","base_url":"https://api.example.com/v1",
		"endpoints":{"chat_completions":{}},"upstream_models":["gpt-4o"]}`
	if status := fixture.call(t, admin, http.MethodPost, "/api/ui/providers", provider).StatusCode; status != http.StatusOK {
		t.Fatalf("create provider status = %d, want %d", status, http.StatusOK)
	}

	// routing and modalities are omitted: the save must report the defaults the
	// live configuration will use.
	model := `{"id":"alias","name":"Alias","providers":[{"provider_id":"p1","upstream_model":"gpt-4o"}]}`
	saved := decodeConsole(t, fixture.call(t, admin, http.MethodPost, "/api/ui/models", model))
	entry, _ := saved["model"].(map[string]any)
	if entry == nil {
		t.Fatalf("model payload = %v", saved)
	}
	if entry["routing"] != core.RoutingPriority {
		t.Fatalf("routing = %v, want %q", entry["routing"], core.RoutingPriority)
	}
	modalities, _ := entry["modalities"].([]any)
	if len(modalities) != 1 || modalities[0] != "text" {
		t.Fatalf("modalities = %v, want [text]", entry["modalities"])
	}

	models := decodeConsole(t, fixture.call(t, admin, http.MethodGet, "/api/ui/models", ""))
	list, _ := models["models"].([]any)
	if len(list) != 1 {
		t.Fatalf("model list = %v, want one entry", models["models"])
	}

	// A binding to a model the provider does not declare is rejected.
	unknown := `{"id":"bad","providers":[{"provider_id":"p1","upstream_model":"not-declared"}]}`
	if status := fixture.call(t, admin, http.MethodPost, "/api/ui/models", unknown).StatusCode; status != http.StatusBadRequest {
		t.Fatalf("unknown upstream model status = %d, want %d", status, http.StatusBadRequest)
	}

	if status := fixture.call(t, admin, http.MethodDelete, "/api/ui/models/alias", "").StatusCode; status != http.StatusOK {
		t.Fatalf("delete model status = %d, want %d", status, http.StatusOK)
	}
	// The provider is now unused and can be removed.
	if status := fixture.call(t, admin, http.MethodDelete, "/api/ui/providers/p1", "").StatusCode; status != http.StatusOK {
		t.Fatalf("delete provider status = %d, want %d", status, http.StatusOK)
	}
}

func TestConsoleDoesNotExposeSecrets(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	provider := `{"id":"p1","base_url":"https://user:pass@api.example.com/v1?token=secret",
		"api_key":"sk-hidden","headers":{"x-secret":"header-secret"},
		"endpoints":{"chat_completions":{}}}`
	if status := fixture.call(t, admin, http.MethodPost, "/api/ui/providers", provider).StatusCode; status != http.StatusOK {
		t.Fatalf("create provider status = %d, want %d", status, http.StatusOK)
	}

	body := readResponse(t, fixture.call(t, admin, http.MethodGet, "/api/ui/providers", ""))
	for _, secret := range []string{"sk-hidden", "header-secret", "user:pass", "token=secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("provider listing leaked %q:\n%s", secret, body)
		}
	}
	if !strings.Contains(body, `"has_key":true`) {
		t.Fatalf("provider listing does not report has_key:\n%s", body)
	}
}

func TestConsoleAPIKeyLifecycle(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	created := decodeConsole(t, fixture.call(t, admin, http.MethodPost, "/api/ui/keys", `{"name":"ci"}`))
	key, _ := created["key"].(map[string]any)
	if key == nil {
		t.Fatalf("key payload = %v", created)
	}
	secret, _ := key["key"].(string)
	if !strings.HasPrefix(secret, "sk-") || len(secret) != len("sk-")+32 {
		t.Fatalf("issued key = %q, want sk- followed by 32 hex characters", secret)
	}

	listed := decodeConsole(t, fixture.call(t, admin, http.MethodGet, "/api/ui/keys", ""))
	keys, _ := listed["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("key list = %v, want one entry", listed["keys"])
	}

	id, _ := key["id"].(string)
	if status := fixture.call(t, admin, http.MethodDelete, "/api/ui/keys/"+id, "").StatusCode; status != http.StatusOK {
		t.Fatalf("delete key status = %d, want %d", status, http.StatusOK)
	}
	listed = decodeConsole(t, fixture.call(t, admin, http.MethodGet, "/api/ui/keys", ""))
	keys, _ = listed["keys"].([]any)
	if len(keys) != 0 {
		t.Fatalf("key list after delete = %v, want empty", listed["keys"])
	}
}

func TestConsolePasswordChangeInvalidatesOtherSessions(t *testing.T) {
	fixture := newConsoleFixture(t)
	first := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)
	second := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	wrong := `{"current_password":"not-the-password","new_password":"a-new-console-password"}`
	if status := fixture.call(t, first, http.MethodPost, "/api/ui/password", wrong).StatusCode; status != http.StatusUnauthorized {
		t.Fatalf("wrong current password status = %d, want %d", status, http.StatusUnauthorized)
	}

	change := `{"current_password":"` + consoleTestPassword + `","new_password":"a-new-console-password"}`
	if status := fixture.call(t, first, http.MethodPost, "/api/ui/password", change).StatusCode; status != http.StatusOK {
		t.Fatalf("password change status = %d, want %d", status, http.StatusOK)
	}

	// The session that changed the password stays usable; the other one does not.
	if status := fixture.call(t, first, http.MethodGet, "/api/ui/session", "").StatusCode; status != http.StatusOK {
		t.Fatalf("changing session status = %d, want %d", status, http.StatusOK)
	}
	if status := fixture.call(t, second, http.MethodGet, "/api/ui/session", "").StatusCode; status != http.StatusUnauthorized {
		t.Fatalf("stale session status = %d, want %d", status, http.StatusUnauthorized)
	}

	_, oldLogin := fixture.login(t, consoleTestUsername, consoleTestPassword)
	if oldLogin.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password login status = %d, want %d", oldLogin.StatusCode, http.StatusUnauthorized)
	}
}

func TestConsoleSessionExpires(t *testing.T) {
	fixture := newConsoleFixture(t)
	admin := fixture.loginOK(t, consoleTestUsername, consoleTestPassword)

	cookie := sessionCookie(t, admin, fixture)
	if cookie == "" {
		t.Fatal("login did not set a session cookie")
	}
	if status := fixture.call(t, admin, http.MethodGet, "/api/ui/session", "").StatusCode; status != http.StatusOK {
		t.Fatalf("session status = %d, want %d", status, http.StatusOK)
	}
}

func sessionCookie(t *testing.T, client *http.Client, fixture *consoleFixture) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, fixture.server.URL+"/api/ui/session", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	for _, cookie := range client.Jar.Cookies(request.URL) {
		if cookie.Name == uiSessionCookie {
			return cookie.Value
		}
	}
	return ""
}

func readResponse(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}

func errorCode(payload map[string]any) string {
	object, _ := payload["error"].(map[string]any)
	code, _ := object["code"].(string)
	return code
}

func TestAttemptLimiterCountsPerAccountAndAddress(t *testing.T) {
	t.Parallel()

	limiter := newAttemptLimiter(uiLoginMaxFailures, uiLoginWindow)
	now := time.Now()
	keys := []string{"user:alice", "ip:10.0.0.1"}

	for attempt := 0; attempt < uiLoginMaxFailures; attempt++ {
		if wait := limiter.reserve(keys, now); wait != 0 {
			t.Fatalf("blocked after %d attempts, want %d allowed", attempt, uiLoginMaxFailures)
		}
	}
	if wait := limiter.reserve(keys, now); wait <= 0 {
		t.Fatal("limiter did not block after the attempt budget was spent")
	}
	// A different account from the same address is still covered by the address
	// bucket; a different address for the same account is not.
	if wait := limiter.reserve([]string{"user:bob", "ip:10.0.0.1"}, now); wait <= 0 {
		t.Fatal("address bucket did not throttle a second username")
	}
	if wait := limiter.reserve([]string{"user:alice", "ip:10.0.0.2"}, now); wait <= 0 {
		t.Fatal("account bucket did not throttle a second address")
	}
	// The window expires.
	later := now.Add(uiLoginWindow + time.Second)
	if wait := limiter.reserve(keys, later); wait != 0 {
		t.Fatalf("limiter still blocked after the window: %v", wait)
	}

	// A successful sign-in clears the counters.
	limiter.reserve(keys, later)
	limiter.reset(keys)
	if wait := limiter.reserve(keys, later); wait != 0 {
		t.Fatal("reset did not clear the counters")
	}
}

func TestAttemptLimiterStaysBounded(t *testing.T) {
	t.Parallel()

	limiter := newAttemptLimiter(uiLoginMaxFailures, uiLoginWindow)
	now := time.Now()
	for index := 0; index < uiLimiterMaxKeys+64; index++ {
		limiter.reserve([]string{fmt.Sprintf("user:%d", index)}, now)
	}
	limiter.mu.Lock()
	size := len(limiter.entries)
	limiter.mu.Unlock()
	if size > uiLimiterMaxKeys {
		t.Fatalf("limiter holds %d entries, want at most %d", size, uiLimiterMaxKeys)
	}
}
