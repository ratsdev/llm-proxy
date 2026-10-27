package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/core"
)

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
