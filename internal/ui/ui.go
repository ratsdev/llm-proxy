package ui

import (
	"net/http"
	"sync"
	"time"

	"github.com/ratsdev/llm-proxy/internal/core"
)

const (
	// RootPath is the console page.
	RootPath        = "/"
	uiAssetsPath    = "/assets/"
	uiAPIPath       = core.APIPrefix + "/ui"
	uiAPIPrefix     = uiAPIPath + "/"
	uiSessionCookie = "llm_proxy_ui_session"
	uiSessionTTL    = 12 * time.Hour
	uiMaxSessions   = 2048
	uiMaxLoginBody  = 8 << 10
	uiHistorySize   = 5000
	uiDefaultLimit  = 200
	uiMaxLimit      = 1000
	uiNoStore       = "no-store"
)

const uiDisabledMessage = "Console access isn't configured for this deployment. Ask your administrator to enable it."

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
	ui             *uiState
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
		ui:             newUIState(),
		logins:         newAttemptLimiter(uiLoginMaxFailures, uiLoginWindow),
		registrations:  newAttemptLimiter(uiRegisterMaxAttempts, uiRegisterWindow),
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
	mux.HandleFunc("GET /{$}", h.handleUIPage)
	mux.Handle("GET "+uiAssetsPath, http.StripPrefix(uiAssetsPath, uiAssetHandler()))
	mux.HandleFunc("GET "+uiAPIPrefix+"session", h.handleUISession)
	mux.HandleFunc("GET "+uiAPIPrefix+"registration", h.handleUIRegistrationStatus)
	mux.HandleFunc("POST "+uiAPIPrefix+"register", h.handleUIRegister)
	mux.HandleFunc("POST "+uiAPIPrefix+"login", h.handleUILogin)
	mux.HandleFunc("POST "+uiAPIPrefix+"logout", h.handleUILogout)
	mux.HandleFunc("GET "+uiAPIPrefix+"settings", h.handleUIGetSettings)
	mux.HandleFunc("PUT "+uiAPIPrefix+"settings", h.handleUISaveSettings)
	mux.HandleFunc("GET "+uiAPIPrefix+"catalog", h.handleUICatalog)
	mux.HandleFunc("GET "+uiAPIPrefix+"overview", h.handleUIOverview)
	mux.HandleFunc("GET "+uiAPIPrefix+"models", h.handleUIListModels)
	mux.HandleFunc("GET "+uiAPIPrefix+"providers", h.handleUIListProviders)
	mux.HandleFunc("GET "+uiAPIPrefix+"users", h.handleUIListUsers)
	mux.HandleFunc("GET "+uiAPIPrefix+"requests", h.handleUIRequests)
	mux.HandleFunc("GET "+uiAPIPrefix+"converters", h.handleUIConverters)
	mux.HandleFunc("POST "+uiAPIPrefix+"password", h.handleUIChangePassword)

	mux.HandleFunc("POST "+uiAPIPrefix+"providers", h.handleUISaveProvider)
	mux.HandleFunc("GET "+uiAPIPrefix+"providers/{id}", h.handleUIGetProvider)
	mux.HandleFunc("PUT "+uiAPIPrefix+"providers/{id}", h.handleUISaveProvider)
	mux.HandleFunc("DELETE "+uiAPIPrefix+"providers/{id}", h.handleUIDeleteProvider)

	mux.HandleFunc("POST "+uiAPIPrefix+"models", h.handleUISaveModel)
	mux.HandleFunc("GET "+uiAPIPrefix+"models/{id}", h.handleUIGetModel)
	mux.HandleFunc("PUT "+uiAPIPrefix+"models/{id}", h.handleUISaveModel)
	mux.HandleFunc("DELETE "+uiAPIPrefix+"models/{id}", h.handleUIDeleteModel)

	mux.HandleFunc("POST "+uiAPIPrefix+"users", h.handleUISaveUser)
	mux.HandleFunc("GET "+uiAPIPrefix+"users/{id}", h.handleUIGetUser)
	mux.HandleFunc("PUT "+uiAPIPrefix+"users/{id}", h.handleUISaveUser)
	mux.HandleFunc("DELETE "+uiAPIPrefix+"users/{id}", h.handleUIDeleteUser)

	mux.HandleFunc("GET "+uiAPIPrefix+"keys", h.handleUIListKeys)
	mux.HandleFunc("POST "+uiAPIPrefix+"keys", h.handleUICreateKey)
	mux.HandleFunc("DELETE "+uiAPIPrefix+"keys/{id}", h.handleUIDeleteKey)

	// Methodless catch-alls keep method errors and unknown UI API paths in the
	// Web UI's JSON envelope instead of ServeMux's plain-text response. The
	// subtree pattern covers every /api/ui path; method-specific routes above
	// always outrank it.
	mux.HandleFunc("/{$}", h.handleUIMethodNotAllowed)
	mux.HandleFunc(uiAssetsPath, h.handleUIMethodNotAllowed)
	mux.HandleFunc(uiAPIPrefix, h.handleUIMethodNotAllowed)
}
