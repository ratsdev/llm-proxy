package llmproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

const maxRequestBodyBytes int64 = 64 << 20

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type routerAPI interface {
	Send(ctx context.Context, request RouteRequest) (*RouteResult, error)
}

type requestInfo struct {
	id       string
	clientID string
}

type requestInfoKey struct{}

// requestLog accumulates the access-log fields a handler wants to report. The
// outermost middleware owns the single "request completed" line, so failures
// that never reach the relay are logged with the same shape as successes.
type requestLog struct {
	enabled       bool
	attrs         []any
	usage         *ir.Usage
	requestID     string
	user          string
	model         string
	provider      string
	upstreamModel string
	inbound       WireFormat
	upstream      WireFormat
	stream        bool
}

type requestLogKey struct{}

func (l *requestLog) add(attrs ...any) {
	l.attrs = append(l.attrs, attrs...)
}

func logFrom(r *http.Request) *requestLog {
	log, _ := r.Context().Value(requestLogKey{}).(*requestLog)
	if log == nil {
		log = &requestLog{}
	}
	return log
}

// Server exposes the HTTP API.
type Server struct {
	cfg       *Config
	router    routerAPI
	logger    *slog.Logger
	handler   http.Handler
	ui        *uiState
	usersByID map[string]*User
	startedAt time.Time
}

// NewServer wires the HTTP API. A nil logger uses slog.Default().
func NewServer(cfg *Config, router routerAPI, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	usersByID := make(map[string]*User, len(cfg.UsersByKey))
	for _, user := range cfg.UsersByKey {
		usersByID[user.ID] = user
	}
	server := &Server{
		cfg:       cfg,
		router:    router,
		logger:    logger,
		ui:        newUIState(),
		usersByID: usersByID,
		startedAt: time.Now(),
	}
	server.handler = server.observe(server.withRequestInfo(server.routes()))
	return server
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
		mux.HandleFunc("POST "+spec.inbound, s.proxyHandler(spec))
		mux.HandleFunc(spec.inbound, s.methodNotAllowed(http.MethodPost))
	}
	s.uiRoutes(mux)
	mux.HandleFunc("/", s.handleNotFound)
	return mux
}

func (s *Server) proxyHandler(spec routeSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.handleProxy(w, r, spec)
	}
}

// methodNotAllowed answers a wrong method on a known path with the gateway's
// JSON error envelope instead of ServeMux's plain-text 405. The methodless
// pattern is always outranked by the method-specific one for valid calls.
func (s *Server) methodNotAllowed(allowed string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logFrom(r).enabled = true
		w.Header().Set("Allow", allowed)
		s.fail(w, r, util.New(http.StatusMethodNotAllowed, "Method Not Allowed.",
			"invalid_request_error", "method_not_allowed", nil))
	}
}

func (s *Server) withRequestInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("x-request-id"))
		clientID := ""
		if requestIDPattern.MatchString(id) {
			clientID = id
		} else {
			id = "req_" + util.RandomHex(32)
		}
		w.Header().Set("x-request-id", id)
		log := logFrom(r)
		log.requestID = id
		log.add("request_id", id)
		ctx := context.WithValue(r.Context(), requestInfoKey{}, requestInfo{id: id, clientID: clientID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// observe is the single outermost middleware: it installs the response
// recorder, recovers panics and writes exactly one access-log line per request.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		requestLog := &requestLog{}
		r = r.WithContext(context.WithValue(r.Context(), requestLogKey{}, requestLog))

		defer func() {
			if value := recover(); value != nil {
				s.logger.Error("panic recovered",
					"panic", value,
					"path", r.URL.Path,
					"stack", string(debug.Stack()))
				if !recorder.wroteHeader {
					s.fail(recorder, r, util.New(500, "Internal server error.", "api_error", "internal_error", nil))
				} else if strings.Contains(recorder.Header().Get("content-type"), "text/event-stream") {
					// A stream is already in flight: give the client a terminal
					// protocol event instead of a silently truncated body.
					frames := adapterFor(wireForPath(r.URL.Path)).
						NewEncoder("", false).EncodeStreamError("Internal server error.")
					for _, frame := range frames {
						if _, err := recorder.Write(frame); err != nil {
							break
						}
					}
				}
			}
			if !requestLog.enabled {
				return
			}
			attrs := append([]any{}, requestLog.attrs...)
			status := recorder.statusCode()
			attrs = append(attrs, "status", status, "duration_ms", time.Since(started).Milliseconds())
			if requestLog.usage != nil {
				attrs = append(attrs, "usage", fmt.Sprintf(
					"input_tokens=%d output_tokens=%d total_tokens=%d",
					requestLog.usage.InputTokens, requestLog.usage.OutputTokens, requestLog.usage.TotalTokens()))
			}
			if requestLog.model != "" {
				s.recordUIRequest(requestLog, started, status)
			}
			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelWarn
			}
			s.logger.Log(r.Context(), level, "request completed", attrs...)
		}()

		next.ServeHTTP(recorder, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	wroteHeader bool
	status      int
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(data)
}

// statusCode reports the status actually sent to the client.
func (r *responseRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// Unwrap lets http.NewResponseController reach the underlying writer.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":      "llm-proxy",
		"version":   Version,
		"models":    len(s.cfg.Models),
		"endpoints": advertisedEndpoints(),
		"ui":        uiRoot,
	})
}

func advertisedEndpoints() map[string][]string {
	endpoints := make(map[string][]string)
	for _, spec := range routeSpecs {
		if spec.advertise {
			key := string(spec.wire)
			endpoints[key] = append(endpoints[key], spec.inbound)
		}
	}
	return endpoints
}

// recordUIRequest feeds the in-memory dashboard window. Only model requests
// reach this path, so health probes and the Web UI itself stay out of the
// traffic statistics.
func (s *Server) recordUIRequest(log *requestLog, started time.Time, status int) {
	record := uiRequestRecord{
		Time:          started.UTC(),
		RequestID:     log.requestID,
		User:          log.user,
		Model:         log.model,
		Provider:      log.provider,
		UpstreamModel: log.upstreamModel,
		Inbound:       string(log.inbound),
		Upstream:      string(log.upstream),
		Stream:        log.stream,
		Status:        status,
		DurationMS:    time.Since(started).Milliseconds(),
	}
	if log.usage != nil {
		record.InputTokens = log.usage.PromptTokens()
		record.OutputTokens = log.usage.OutputTokens
		record.TotalTokens = log.usage.TotalTokens()
	}
	s.ui.recordRequest(record)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "healthy",
		"models":    len(s.cfg.Models),
		"providers": len(s.cfg.Providers),
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	logFrom(r).enabled = true
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found"})
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	log := logFrom(r)
	log.enabled = true

	user, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.user = userID(user)
	log.add("user", userID(user))

	ids := make([]string, 0, len(s.cfg.Models))
	for id, model := range s.cfg.Models {
		if user == nil || user.CanUse(model.ID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	data := make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, modelPayload(s.cfg.Models[id]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
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
	log.add("user", userID(user))
	model, apiErr := s.resolveModel(r.PathValue("model_id"), user)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.add("model", model.ID)
	writeJSON(w, http.StatusOK, modelPayload(model))
}

func modelPayload(model *Model) map[string]any {
	payload := map[string]any{
		"id":         model.ID,
		"object":     "model",
		"created":    time.Now().Unix(),
		"owned_by":   "llm-proxy",
		"modalities": model.Modalities,
	}
	payload["context_length"] = nil
	if model.ContextLength != nil {
		payload["context_length"] = *model.ContextLength
	}
	payload["max_output_length"] = nil
	if model.MaxOutputLength != nil {
		payload["max_output_length"] = *model.MaxOutputLength
	}
	return payload
}

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

func (s *Server) fail(w http.ResponseWriter, r *http.Request, gatewayError *util.Error) {
	for key, value := range gatewayError.Headers {
		w.Header().Set(key, value)
	}
	adapter := adapterFor(wireForPath(r.URL.Path))
	payload := adapter.FormatError(gatewayError.Status, gatewayError.Message,
		gatewayError.Type, gatewayError.Code, requestInfoFrom(r).id)
	writeJSON(w, gatewayError.Status, payload)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	encoded, err := util.Marshal(payload)
	if err != nil {
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func readJSONBody(w http.ResponseWriter, r *http.Request) (map[string]any, *util.Error) {
	body := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return nil, util.New(413, "Request body too large.", "", "request_too_large", nil)
		}
		return nil, util.New(400, "Request body is not valid JSON.", "", "invalid_json", nil)
	}
	value, err := util.Decode(data)
	if err != nil {
		return nil, util.New(400, "Request body is not valid JSON.", "", "invalid_json", nil)
	}
	object, ok := util.AsMap(value)
	if !ok {
		return nil, util.New(400, "Request body must be a JSON object.", "", "invalid_body", nil)
	}
	return object, nil
}

func inboundHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string, 2)
	for _, name := range []string{"anthropic-version", "anthropic-beta"} {
		if value := r.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return headers
}

func requestInfoFrom(r *http.Request) requestInfo {
	info, _ := r.Context().Value(requestInfoKey{}).(requestInfo)
	if info.id == "" {
		info.id = "req_" + util.RandomHex(32)
	}
	return info
}

func userID(user *User) string {
	if user == nil {
		return "-"
	}
	return user.ID
}
