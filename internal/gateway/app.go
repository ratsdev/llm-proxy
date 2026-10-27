package gateway

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ratsdev/llm-proxy/internal/router"
	"github.com/ratsdev/llm-proxy/internal/ui"
)

// App is the top-level HTTP handler. It owns the database, the live
// configuration snapshot and the Web UI handler, and swaps the whole routing
// stack after an administrative change.
//
// The routing memory (conversation pins, 429 cooldowns, round-robin cursors) and
// the upstream connection pool are owned here rather than by a Router, so a
// configuration change replaces the routing stack without losing affinity or
// forcing every upstream connection to be re-established.
type App struct {
	store  *Store
	logger *slog.Logger
	ui     *ui.Handler

	reloadMu sync.Mutex
	mu       sync.RWMutex
	current  *appState
	config   *Config
	started  time.Time

	routing *router.State
	pool    *router.TransportPool
}

type appState struct {
	handler http.Handler
	router  *router.Router
}

// NewApp loads the current configuration from the store and builds the first
// routing stack.
func NewApp(store *Store, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	app := &App{
		store:   store,
		logger:  logger,
		started: time.Now(),
		routing: router.NewState(),
		pool:    router.NewTransportPool(),
	}
	app.ui = newUIHandler(app.Config, store, app.Reload, app.started)
	if err := app.Reload(); err != nil {
		return nil, err
	}
	return app, nil
}

// Reload rebuilds the configuration snapshot and HTTP stack. In-flight
// requests keep the stack they started with. The routing memory and the
// upstream connection pool are shared with the previous generation, so a
// configuration change never moves a conversation to another provider.
func (a *App) Reload() error {
	// Two concurrent administrative writes must not race to install an older
	// snapshot: whoever read the database last wins.
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	cfg, err := a.store.LoadConfig()
	if err != nil {
		return err
	}
	routing, err := router.NewRouter(cfg,
		router.WithLogger(a.logger),
		router.WithState(a.routing),
		router.WithTransportPool(a.pool),
	)
	if err != nil {
		return err
	}
	server := NewServerWithOptions(cfg, routing, a.logger, ServerOptions{
		Store:     a.store,
		Reload:    a.Reload,
		UI:        a.ui,
		StartedAt: a.started,
	})
	state := &appState{handler: server.Handler(), router: routing}

	a.mu.Lock()
	previous := a.current
	a.current = state
	a.config = cfg
	a.mu.Unlock()

	// The new stack reuses the shared pool, so in-flight requests keep their
	// sockets and only the proxies that are no longer configured are released.
	a.pool.Prune(configuredProxies(cfg))
	a.routing.PruneModels(configuredModels(cfg))
	if previous != nil {
		previous.router.Close()
	}
	return nil
}

// configuredProxies lists the proxy URLs the current configuration can use, so
// the pool can drop the clients of providers that were removed.
func configuredProxies(cfg *Config) map[string]bool {
	proxies := map[string]bool{"": true}
	for _, provider := range cfg.Providers {
		if provider.Proxy != "" {
			proxies[provider.Proxy] = true
		}
	}
	return proxies
}

// configuredModels lists the model aliases the current configuration serves, so
// the routing memory can drop state for deleted models.
func configuredModels(cfg *Config) map[string]bool {
	models := make(map[string]bool, len(cfg.Models))
	for id := range cfg.Models {
		models[id] = true
	}
	return models
}

// ServeHTTP delegates to the current stack.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	state := a.current
	a.mu.RUnlock()
	if state == nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	state.handler.ServeHTTP(w, r)
}

// Config returns the latest configuration snapshot.
func (a *App) Config() *Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config
}

// Close stops serving and releases the shared upstream connection pool.
func (a *App) Close() {
	a.mu.Lock()
	state := a.current
	a.current = nil
	a.mu.Unlock()
	if state != nil {
		state.router.Close()
	}
	a.pool.Close()
}
