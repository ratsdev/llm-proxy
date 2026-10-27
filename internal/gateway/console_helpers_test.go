package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/core"
)

// Console (Web UI) fixtures and HTTP helpers shared by console_test.go and
// console_admin_test.go.

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

func uiDecode(t *testing.T, response *http.Response) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(uiBody(t, response)), &object); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return object
}

func uiLogin(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	return uiLoginWithPassword(t, server, testAdminUsername, testAdminPassword)
}

func uiLoginWithPassword(t *testing.T, server *httptest.Server, username, password string) *http.Client {
	t.Helper()
	client := uiClient(t, server)
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	response := uiCall(t, client, http.MethodPost, server.URL+"/api/ui/login", body, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d: %s", response.StatusCode, uiBody(t, response))
	}
	return client
}

// newAdminTestServer boots a full App on a temporary database, which is what
// the administrator flows need: real sessions and real store writes.
func newAdminTestServer(t *testing.T) (*httptest.Server, *App, string) {
	t.Helper()

	store, err := OpenStore(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.RegisterAdmin(core.UserInput{
		ID:       testAdminUsername,
		Name:     "Administrator",
		Password: testAdminPassword,
	}); err != nil {
		t.Fatalf("register admin: %v", err)
	}
	app, err := NewApp(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(app.Close)
	server := httptest.NewServer(app)
	t.Cleanup(server.Close)
	return server, app, testAdminPassword
}
