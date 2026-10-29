package ui

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ratsdev/llm-proxy/internal/core"
	"github.com/ratsdev/llm-proxy/internal/util"
)

const (
	// RootPath is the console page.
	RootPath      = "/"
	assetsPath    = "/assets/"
	apiPath       = core.APIPrefix + "/ui"
	apiPrefix     = apiPath + "/"
	sessionCookie = "llm_proxy_ui_session"
	sessionTTL    = 12 * time.Hour
	maxSessions   = 2048
	maxLoginBody  = 8 << 10
	historySize   = 5000
	defaultLimit  = 200
	maxLimit      = 1000
	noStore       = "no-store"
)

const disabledMessage = "Console access isn't configured for this deployment. Ask your administrator to enable it."

// Options configures the console handler. Config is called on every request so
// a running handler observes hot-reloaded gateway configuration.
type Options struct {
	Config         func() *Config
	Store          *Store
	Reload         func() error
	StartedAt      time.Time
	Version        string
	DefaultTimeout time.Duration
	// LogLogin is called at the start of a login attempt with an empty user ID,
	// and again with the authenticated account. The gateway uses it to emit the
	// access-log line for console logins.
	LogLogin func(*http.Request, string)
}

// Handler serves the embedded Web console and its JSON API.
type Handler struct {
	configFn       func() *Config
	store          *Store
	reload         func() error
	startedAt      time.Time
	version        string
	defaultTimeout time.Duration
	onLogin        func(*http.Request, string)
	ui             *state
	logins         *attemptLimiter
	registrations  *attemptLimiter

	// usersByID merges the two user indexes of a configuration snapshot once
	// per snapshot instead of once per request.
	usersMu  sync.Mutex
	usersFor *Config
	users    map[string]*User
}

// New builds a console handler. The configuration callback lets a running
// handler observe hot-reloaded gateway configuration.
func New(opts Options) *Handler {
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Handler{
		configFn:       opts.Config,
		store:          opts.Store,
		reload:         opts.Reload,
		startedAt:      startedAt,
		version:        opts.Version,
		defaultTimeout: opts.DefaultTimeout,
		onLogin:        opts.LogLogin,
		ui:             newState(),
		logins:         newAttemptLimiter(loginMaxFailures, loginWindow),
		registrations:  newAttemptLimiter(registerMaxAttempts, registerWindow),
	}
}

// RecordRequest appends one completed model request to the in-memory history.
func (h *Handler) RecordRequest(record RequestRecord) {
	h.ui.recordRequest(record)
}

func (h *Handler) config() *Config {
	if h.configFn == nil {
		return &Config{}
	}
	if cfg := h.configFn(); cfg != nil {
		return cfg
	}
	return &Config{}
}

// usersByID merges both user indexes so accounts loaded from either side of the
// configuration snapshot can sign in. The result is cached per snapshot: a
// configuration change installs a new *Config, which invalidates it.
func (h *Handler) usersByID() map[string]*User {
	cfg := h.config()
	h.usersMu.Lock()
	defer h.usersMu.Unlock()
	if h.usersFor == cfg && h.users != nil {
		return h.users
	}
	users := make(map[string]*User, len(cfg.UsersByID)+len(cfg.UsersByKey))
	for id, user := range cfg.UsersByID {
		users[id] = user
	}
	for _, user := range cfg.UsersByKey {
		users[user.ID] = user
	}
	h.usersFor, h.users = cfg, users
	return users
}

// Register mounts the console page, its assets and the JSON API.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.handlePage)
	mux.Handle("GET "+assetsPath, http.StripPrefix(assetsPath, assetHandler()))
	mux.HandleFunc("GET "+apiPrefix+"session", h.handleSession)
	mux.HandleFunc("GET "+apiPrefix+"registration", h.handleRegistrationStatus)
	mux.HandleFunc("POST "+apiPrefix+"register", h.handleRegister)
	mux.HandleFunc("POST "+apiPrefix+"login", h.handleLogin)
	mux.HandleFunc("POST "+apiPrefix+"logout", h.handleLogout)
	mux.HandleFunc("GET "+apiPrefix+"settings", h.handleGetSettings)
	mux.HandleFunc("PUT "+apiPrefix+"settings", h.handleSaveSettings)
	mux.HandleFunc("GET "+apiPrefix+"catalog", h.handleCatalog)
	mux.HandleFunc("GET "+apiPrefix+"overview", h.handleOverview)
	mux.HandleFunc("GET "+apiPrefix+"models", h.handleListModels)
	mux.HandleFunc("GET "+apiPrefix+"providers", h.handleListProviders)
	mux.HandleFunc("GET "+apiPrefix+"users", h.handleListUsers)
	mux.HandleFunc("GET "+apiPrefix+"requests", h.handleRequests)
	mux.HandleFunc("GET "+apiPrefix+"converters", h.handleConverters)
	mux.HandleFunc("POST "+apiPrefix+"password", h.handleChangePassword)

	mux.HandleFunc("POST "+apiPrefix+"providers", h.handleSaveProvider)
	mux.HandleFunc("GET "+apiPrefix+"providers/{id}", h.handleGetProvider)
	mux.HandleFunc("PUT "+apiPrefix+"providers/{id}", h.handleSaveProvider)
	mux.HandleFunc("DELETE "+apiPrefix+"providers/{id}", h.handleDeleteProvider)

	mux.HandleFunc("POST "+apiPrefix+"models", h.handleSaveModel)
	mux.HandleFunc("GET "+apiPrefix+"models/{id}", h.handleGetModel)
	mux.HandleFunc("PUT "+apiPrefix+"models/{id}", h.handleSaveModel)
	mux.HandleFunc("DELETE "+apiPrefix+"models/{id}", h.handleDeleteModel)

	mux.HandleFunc("POST "+apiPrefix+"users", h.handleSaveUser)
	mux.HandleFunc("GET "+apiPrefix+"users/{id}", h.handleGetUser)
	mux.HandleFunc("PUT "+apiPrefix+"users/{id}", h.handleSaveUser)
	mux.HandleFunc("DELETE "+apiPrefix+"users/{id}", h.handleDeleteUser)

	mux.HandleFunc("GET "+apiPrefix+"keys", h.handleListKeys)
	mux.HandleFunc("POST "+apiPrefix+"keys", h.handleCreateKey)
	mux.HandleFunc("DELETE "+apiPrefix+"keys/{id}", h.handleDeleteKey)

	// Methodless catch-alls keep method errors and unknown UI API paths in the
	// Web UI's JSON envelope instead of ServeMux's plain-text response. The
	// subtree pattern covers every /api/ui path; method-specific routes above
	// always outrank it.
	mux.HandleFunc("/{$}", h.handleMethodNotAllowed)
	mux.HandleFunc(assetsPath, h.handleMethodNotAllowed)
	mux.HandleFunc(apiPrefix, h.handleMethodNotAllowed)
}

// HTTP plumbing shared by the console handlers.

func (h *Handler) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "Method not allowed.", "method_not_allowed")
}

// parseInt reads a bounded query parameter, falling back to the default when
// the value is missing or unparsable.
func parseInt(raw string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return min(max(value, minimum), maximum)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("cache-control", noStore)
	w.Header().Set("x-content-type-options", "nosniff")
	util.WriteJSON(w, status, payload)
}

func writeError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"code":    code,
		},
	})
}

// The console works with the shared configuration types from core.

type (
	Config        = core.Config
	Provider      = core.Provider
	ModelProvider = core.ModelProvider
	Model         = core.Model
	User          = core.User
	Store         = core.Store
	ProviderInput = core.ProviderInput
	ModelInput    = core.ModelInput
	UserInput     = core.UserInput
)

var ErrNotFound = core.ErrNotFound
