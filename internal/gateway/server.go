package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ratsdev/llm-proxy/internal/router"
	"github.com/ratsdev/llm-proxy/internal/ui"
	"github.com/ratsdev/llm-proxy/internal/util"
)

type routerAPI interface {
	Send(ctx context.Context, request RouteRequest) (*RouteResult, error)
}

// Server exposes the HTTP API.
type Server struct {
	cfg       *Config
	router    routerAPI
	logger    *slog.Logger
	handler   http.Handler
	ui        *ui.Handler
	startedAt time.Time
}

// ServerOptions wires the store-backed management endpoints. Leaving UI nil
// builds a static console handler, which keeps the package usable in tests.
type ServerOptions struct {
	Store     *Store
	Reload    func() error
	UI        *ui.Handler
	StartedAt time.Time
}

// NewServerWithOptions wires the HTTP API. A nil logger uses slog.Default();
// when opts.UI is nil the server builds a static console handler of its own.
func NewServerWithOptions(cfg *Config, router routerAPI, logger *slog.Logger, opts ServerOptions) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if opts.UI == nil {
		// Tests and embedders that do not reload configuration can use a static
		// snapshot; App always passes the shared hot-reloading handler.
		opts.UI = newUIHandler(func() *Config { return cfg }, opts.Store, opts.Reload, startedAt)
	}
	server := &Server{
		cfg:       cfg,
		router:    router,
		logger:    logger,
		ui:        opts.UI,
		startedAt: startedAt,
	}
	server.handler = server.observe(server.withRequestInfo(server.routes()))
	return server
}

// newUIHandler builds the console handler. Both App and a directly constructed
// Server go through it so the two wiring paths cannot drift apart.
func newUIHandler(cfg func() *Config, store *Store, reload func() error, startedAt time.Time) *ui.Handler {
	return ui.New(ui.Options{
		Config:         cfg,
		Store:          store,
		Reload:         reload,
		StartedAt:      startedAt,
		Version:        Version,
		DefaultTimeout: router.DefaultReadTimeout,
		LogLogin:       logUILogin,
	})
}

// logUILogin reports console login activity through the gateway access log.
// Both outcomes are logged: a rejected attempt is exactly what an operator
// wants to see, and a successful one establishes who opened the console. The
// handler reports the attempt before authenticating and again with the account
// on success, so the "ui" attribute is added only once.
func logUILogin(r *http.Request, userID string) {
	log := logFrom(r)
	if !log.enabled {
		log.enabled = true
		log.add("ui", "login")
	}
	log.user = userID
}

// Handler returns the HTTP handler, including middleware.
func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apiPrefix, s.handleIndex)
	mux.HandleFunc("GET "+apiPrefix+"/{$}", s.handleIndex)
	mux.HandleFunc(apiPrefix, s.methodNotAllowed(http.MethodGet))
	mux.HandleFunc(apiPrefix+"/{$}", s.methodNotAllowed(http.MethodGet))
	mux.HandleFunc("GET "+apiPrefix+"/health", s.handleHealth)
	mux.HandleFunc(apiPrefix+"/health", s.methodNotAllowed(http.MethodGet))
	mux.HandleFunc("GET "+apiPrefix+"/v1/models", s.handleListModels)
	mux.HandleFunc(apiPrefix+"/v1/models", s.methodNotAllowed(http.MethodGet))
	mux.HandleFunc("GET "+apiPrefix+"/v1/models/{model_id...}", s.handleRetrieveModel)
	mux.HandleFunc(apiPrefix+"/v1/models/{model_id...}", s.methodNotAllowed(http.MethodGet))
	for _, spec := range routeSpecs {
		mux.HandleFunc("POST "+spec.Inbound, s.proxyHandler(spec))
		mux.HandleFunc(spec.Inbound, s.methodNotAllowed(http.MethodPost))
	}
	s.ui.Register(mux)
	mux.HandleFunc("/", s.handleNotFound)
	return mux
}

func (s *Server) proxyHandler(spec routeSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.handleProxy(w, r, spec)
	}
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	logFrom(r).enabled = true
	util.WriteJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found"})
}
