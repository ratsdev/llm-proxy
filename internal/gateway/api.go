package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
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

// Handler returns the HTTP handler, including middleware.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// routes builds the route table from the canonical route specs, so the
// advertised endpoints and the served endpoints cannot disagree.
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

// The index document describes the gateway itself: version, route table and the
// converters an operator can select.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	util.WriteJSON(w, http.StatusOK, map[string]any{
		"name":       "llm-proxy",
		"version":    Version,
		"models":     len(s.cfg.Models),
		"endpoints":  advertisedEndpoints(),
		"converters": convert.Definitions(),
		"ui":         ui.RootPath,
	})
}

func advertisedEndpoints() map[string][]string {
	endpoints := make(map[string][]string)
	for _, spec := range routeSpecs {
		if spec.Advertise {
			key := string(spec.Wire)
			endpoints[key] = append(endpoints[key], spec.Inbound)
		}
	}
	return endpoints
}

// handleHealth reports liveness and the size of the loaded snapshot.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	util.WriteJSON(w, http.StatusOK, map[string]any{
		"status":    "healthy",
		"models":    len(s.cfg.Models),
		"providers": len(s.cfg.Providers),
	})
}

// Model listing for the OpenAI-compatible catalogue endpoint.

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	log := logFrom(r)
	log.enabled = true

	user, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.user = userID(user)

	ids := make([]string, 0, len(s.cfg.Models))
	for id, model := range s.cfg.Models {
		if user == nil || user.CanUse(model.ID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	data := make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, modelPayload(s.cfg.Models[id], s.startedAt))
	}
	util.WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleRetrieveModel(w http.ResponseWriter, r *http.Request) {
	log := logFrom(r)
	log.enabled = true

	user, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.user = userID(user)
	model, apiErr := s.resolveModel(r.PathValue("model_id"), user)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.model = model.ID
	util.WriteJSON(w, http.StatusOK, modelPayload(model, s.startedAt))
}

// modelPayload renders one model entry. created is the process start time, so
// the listing is stable for the lifetime of the process instead of changing on
// every call.
func modelPayload(model *Model, created time.Time) map[string]any {
	return map[string]any{
		"id":                model.ID,
		"name":              model.Name,
		"object":            "model",
		"created":           created.Unix(),
		"owned_by":          "llm-proxy",
		"modalities":        model.Modalities,
		"output_modalities": model.OutputModalities,
		"reasoning":         model.Reasoning,
		"tools":             model.Tools,
		"context_length":    util.Nullable(model.ContextLength),
		"max_output_length": util.Nullable(model.MaxOutputLength),
	}
}

// Request helpers: JSON bodies and error envelopes in the inbound protocol's shape.

const maxRequestBodyBytes int64 = 64 << 20

// fail renders a gateway error in the inbound protocol's error envelope.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, gatewayError *util.Error) {
	for key, value := range gatewayError.Headers {
		w.Header().Set(key, value)
	}
	payload := convert.FormatError(wireForPath(r.URL.Path),
		gatewayError.Status, gatewayError.Message,
		gatewayError.Type, gatewayError.Code, requestInfoFrom(r).id)
	util.WriteJSON(w, gatewayError.Status, payload)
}

// readJSONBody reads the request body and renders failures in the gateway's
// JSON error envelope.
func readJSONBody(w http.ResponseWriter, r *http.Request) (map[string]any, *util.Error) {
	object, err := util.ReadJSONObject(w, r, maxRequestBodyBytes)
	switch {
	case err == nil:
		return object, nil
	case errors.Is(err, util.ErrBodyTooLarge):
		return nil, util.New(413, "Request body too large.", "", "request_too_large", nil)
	case errors.Is(err, util.ErrBodyNotObject):
		return nil, util.New(400, "Request body must be a JSON object.", "", "invalid_body", nil)
	default:
		return nil, util.New(400, "Request body is not valid JSON.", "", "invalid_json", nil)
	}
}

// inboundHeaders forwards the Anthropic protocol headers a caller may set.
func inboundHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string, 2)
	for _, name := range []string{"anthropic-version", "anthropic-beta"} {
		if value := r.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return headers
}

// authenticate resolves the caller's API key. An empty key table disables
// authentication entirely, which is what a fresh deployment looks like.
func (s *Server) authenticate(r *http.Request) (*User, *util.Error) {
	if !s.cfg.AuthEnabled() {
		return nil, nil
	}
	authorization := r.Header.Get("Authorization")
	var token string
	if strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		token = strings.TrimSpace(authorization[7:])
	} else {
		token = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if token == "" {
		return nil, util.New(401,
			"Missing API key. Pass it as 'Authorization: Bearer <key>' or 'x-api-key'.",
			"authentication_error", "missing_api_key", nil)
	}
	user, ok := s.cfg.UsersByKey[token]
	if !ok {
		return nil, util.New(401, "Invalid API key.", "authentication_error", "invalid_api_key", nil)
	}
	return user, nil
}

// resolveModel enforces the account's model allow-list.
func (s *Server) resolveModel(modelID string, user *User) (*Model, *util.Error) {
	model, ok := s.cfg.Models[modelID]
	if !ok {
		return nil, util.New(404, fmt.Sprintf("Model '%s' not found.", modelID),
			"model_not_found", "model_not_found", nil)
	}
	if user != nil && !user.CanUse(model.ID) {
		return nil, util.New(403, fmt.Sprintf("User '%s' is not allowed to use model '%s'.", user.ID, model.ID),
			"permission_error", "model_not_allowed", nil)
	}
	return model, nil
}

func userID(user *User) string {
	if user == nil {
		return "-"
	}
	return user.ID
}
