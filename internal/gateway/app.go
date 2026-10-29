package gateway

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/core"
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

// Core types live in the core package so the gateway and the console can share
// them without an import cycle, and the routing engine lives in its own package
// so it can be tested without an HTTP stack. These aliases keep the gateway
// wiring readable.

type (
	Config           = core.Config
	Provider         = core.Provider
	ProviderEndpoint = core.ProviderEndpoint
	ModelProvider    = core.ModelProvider
	Model            = core.Model
	User             = core.User
	Store            = core.Store
	WireFormat       = core.WireFormat
	routeSpec        = core.RouteSpec

	// RouteRequest and RouteResult are the routing engine's I/O types; the
	// gateway hands requests in and relays whatever comes back.
	RouteRequest = router.RouteRequest
	RouteResult  = router.RouteResult

	// Usage is the protocol-neutral token accounting shared with the
	// conversion layer.
	Usage = ir.Usage
)

const (
	DefaultDatabasePath = core.DefaultDatabasePath
	RoutingPriority     = core.RoutingPriority
	RoutingRoundRobin   = core.RoutingRoundRobin
	RoutingRandom       = core.RoutingRandom

	wireChatCompletions = core.WireChatCompletions
	wireResponses       = core.WireResponses
	wireMessages        = core.WireMessages
	apiPrefix           = core.APIPrefix
)

var routeSpecs = core.RouteSpecs()

// Version is the version reported by the HTTP API and the console. The value
// itself lives in core because it is patched at build time.
var Version = core.Value

// OpenStore opens (and creates when necessary) the SQLite database.
func OpenStore(path string) (*Store, error) {
	return core.OpenStore(path)
}

// wireForPath reports the wire format an inbound path speaks.
func wireForPath(path string) WireFormat {
	return core.WireForPath(path)
}
