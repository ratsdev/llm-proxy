package llmproxy

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ratsdev/llm-proxy/util"
)

const (
	uiRoot          = "/ui"
	uiAssetsPath    = "/ui/assets/"
	uiAPIPath       = apiPrefix + "/ui"
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

//go:embed assets/*
var uiAssetFS embed.FS

var (
	uiAssets     = mustUIAssetFS()
	uiIndexHTML  = mustUIAsset("index.html")
	uiAssetETags = buildUIAssetETags()
	uiHTMLHeader = map[string]string{
		"cache-control":          uiNoStore,
		"x-content-type-options": "nosniff",
		"x-frame-options":        "DENY",
		"referrer-policy":        "no-referrer",
		"content-security-policy": "default-src 'self'; img-src 'self' data:; " +
			"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; " +
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
	}
)

func mustUIAssetFS() fs.FS {
	sub, err := fs.Sub(uiAssetFS, "assets")
	if err != nil {
		panic("llm-proxy: embedded Web UI assets unavailable: " + err.Error())
	}
	return sub
}

func mustUIAsset(name string) []byte {
	data, err := fs.ReadFile(uiAssets, name)
	if err != nil {
		panic("llm-proxy: embedded Web UI asset missing: " + name)
	}
	return data
}

// buildUIAssetETags fingerprints the embedded bundle once at startup. Without a
// validator the browser has to re-download every asset each time the console
// loads, because embed.FS files carry no modification time.
func buildUIAssetETags() map[string]string {
	tags := make(map[string]string)
	err := fs.WalkDir(uiAssets, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := fs.ReadFile(uiAssets, name)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		tags[name] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	if err != nil {
		panic("llm-proxy: embedded Web UI assets unreadable: " + err.Error())
	}
	return tags
}

type uiSession struct {
	userID    string
	expiresAt time.Time
}

// uiRequestRecord is one completed model request kept in the in-memory window
// that feeds the Web UI dashboard and log view.
type uiRequestRecord struct {
	Time          time.Time `json:"time"`
	RequestID     string    `json:"request_id"`
	User          string    `json:"user"`
	Model         string    `json:"model"`
	ModelName     string    `json:"model_name"`
	Provider      string    `json:"provider"`
	ProviderName  string    `json:"provider_name"`
	UpstreamModel string    `json:"upstream_model"`
	Inbound       string    `json:"inbound"`
	Upstream      string    `json:"upstream"`
	Stream        bool      `json:"stream"`
	Status        int       `json:"status"`
	DurationMS    int64     `json:"duration_ms"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	TotalTokens   int       `json:"total_tokens"`
}

type uiBreakdown struct {
	Name          string  `json:"name"`
	Requests      int     `json:"requests"`
	Errors        int     `json:"errors"`
	TotalTokens   int     `json:"total_tokens"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
}

type uiSummary struct {
	Requests      int     `json:"requests"`
	Errors        int     `json:"errors"`
	Success       int     `json:"success"`
	SuccessRate   float64 `json:"success_rate"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	TotalTokens   int     `json:"total_tokens"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
}

type uiBucket struct {
	Start       time.Time `json:"start"`
	Requests    int       `json:"requests"`
	Errors      int       `json:"errors"`
	TotalTokens int       `json:"total_tokens"`
}

// uiState holds the in-memory session table and the bounded request history.
// Neither survives a restart: the gateway stays stateless.
type uiState struct {
	mu       sync.Mutex
	sessions map[string]uiSession
	records  []uiRequestRecord
	next     int
}

func newUIState() *uiState {
	return &uiState{
		sessions: make(map[string]uiSession),
		records:  make([]uiRequestRecord, 0, uiHistorySize),
	}
}

func (u *uiState) createSession(userID string, now time.Time) (string, time.Time) {
	token := "ui_" + util.RandomHex(48)
	expires := now.Add(uiSessionTTL)

	u.mu.Lock()
	defer u.mu.Unlock()
	for key, session := range u.sessions {
		if !session.expiresAt.After(now) {
			delete(u.sessions, key)
		}
	}
	if len(u.sessions) >= uiMaxSessions {
		oldestKey := ""
		var oldest time.Time
		for key, session := range u.sessions {
			if oldestKey == "" || session.expiresAt.Before(oldest) {
				oldestKey, oldest = key, session.expiresAt
			}
		}
		if oldestKey != "" {
			delete(u.sessions, oldestKey)
		}
	}
	u.sessions[token] = uiSession{userID: userID, expiresAt: expires}
	return token, expires
}

func (u *uiState) lookupSession(token string, now time.Time) (uiSession, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	session, ok := u.sessions[token]
	if !ok {
		return uiSession{}, false
	}
	if !session.expiresAt.After(now) {
		delete(u.sessions, token)
		return uiSession{}, false
	}
	return session, true
}

func (u *uiState) deleteSession(token string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.sessions, token)
}

func (u *uiState) recordRequest(record uiRequestRecord) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.records) < uiHistorySize {
		u.records = append(u.records, record)
		return
	}
	u.records[u.next] = record
	u.next = (u.next + 1) % uiHistorySize
}

// historyFull reports whether the shared window has started evicting records.
func (u *uiState) historyFull() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.records) >= uiHistorySize
}

// snapshotRecordsFor returns one user's history newest-first. Filtering happens
// while the lock is held so other users' records never leave the critical
// section and only one copy of the window is made.
func (u *uiState) snapshotRecordsFor(userID string) []uiRequestRecord {
	u.mu.Lock()
	defer u.mu.Unlock()

	size := len(u.records)
	out := make([]uiRequestRecord, 0, size)
	for offset := 0; offset < size; offset++ {
		record := u.records[(u.next+size-offset-1)%size]
		if record.User == userID {
			out = append(out, record)
		}
	}
	return out
}

func (s *Server) uiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+uiRoot, s.handleUIPage)
	mux.HandleFunc("GET "+uiRoot+"/{$}", s.handleUIPage)
	mux.Handle("GET "+uiAssetsPath, http.StripPrefix(uiAssetsPath, uiAssetHandler()))
	mux.HandleFunc("GET "+uiAPIPrefix+"session", s.handleUISession)
	mux.HandleFunc("POST "+uiAPIPrefix+"login", s.handleUILogin)
	mux.HandleFunc("POST "+uiAPIPrefix+"logout", s.handleUILogout)
	mux.HandleFunc("GET "+uiAPIPrefix+"overview", s.handleUIOverview)
	mux.HandleFunc("GET "+uiAPIPrefix+"models", s.handleUIListModels)
	mux.HandleFunc("GET "+uiAPIPrefix+"providers", s.handleUIListProviders)
	mux.HandleFunc("GET "+uiAPIPrefix+"requests", s.handleUIRequests)

	// Methodless catch-alls keep method errors and unknown UI API paths in the
	// Web UI's JSON envelope instead of ServeMux's plain-text response.
	mux.HandleFunc(uiRoot, s.handleUIMethodNotAllowed)
	mux.HandleFunc(uiRoot+"/{$}", s.handleUIMethodNotAllowed)
	mux.HandleFunc(uiAssetsPath, s.handleUIMethodNotAllowed)
	mux.HandleFunc(uiAPIPrefix, s.handleUIMethodNotAllowed)
}

func uiAssetHandler() http.Handler {
	files := http.FileServer(http.FS(uiAssets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := uiAssetETags[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Header().Set("etag", tag)
		}
		w.Header().Set("x-content-type-options", "nosniff")
		w.Header().Set("cache-control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func (s *Server) handleUIPage(w http.ResponseWriter, r *http.Request) {
	for key, value := range uiHTMLHeader {
		w.Header().Set(key, value)
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(uiIndexHTML)
}

func (s *Server) handleUIMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeUIError(w, http.StatusMethodNotAllowed, "Method not allowed.", "method_not_allowed")
}

func (s *Server) handleUILogin(w http.ResponseWriter, r *http.Request) {
	log := logFrom(r)
	log.enabled = true
	log.add("ui", "login")

	if !s.cfg.AuthEnabled() {
		writeUIError(w, http.StatusForbidden, uiDisabledMessage, "ui_disabled")
		return
	}

	body := http.MaxBytesReader(w, r.Body, uiMaxLoginBody)
	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeUIError(w, http.StatusRequestEntityTooLarge, "Request body is too large.", "invalid_body")
			return
		}
		writeUIError(w, http.StatusBadRequest, "Could not read the request body.", "invalid_body")
		return
	}
	value, err := util.Decode(data)
	object, ok := util.AsMap(value)
	if err != nil || !ok {
		writeUIError(w, http.StatusBadRequest, "Request body must be a JSON object.", "invalid_json")
		return
	}

	key := strings.TrimSpace(util.StringValue(object["api_key"]))
	if key == "" {
		writeUIError(w, http.StatusBadRequest, "Enter your API key to continue.", "missing_api_key")
		return
	}
	user, ok := s.cfg.UsersByKey[key]
	if !ok {
		writeUIError(w, http.StatusUnauthorized, "The API key you entered isn't valid.", "invalid_api_key")
		return
	}

	token, expires := s.ui.createSession(user.ID, time.Now())
	log.user = user.ID
	log.add("user", user.ID)
	s.setUISessionCookie(w, r, token, expires)
	writeUIJSON(w, http.StatusOK, map[string]any{
		"user":       s.userPayload(user),
		"expires_at": expires.UTC(),
		"gateway":    uiGatewayPayload(),
	})
}

func (s *Server) handleUILogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(uiSessionCookie); err == nil {
		s.ui.deleteSession(cookie.Value)
	}
	s.clearUISessionCookie(w, r)
	writeUIJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUISession(w http.ResponseWriter, r *http.Request) {
	user, expires, ok := s.requireUIUser(w, r)
	if !ok {
		return
	}
	writeUIJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          s.userPayload(user),
		"expires_at":    expires.UTC(),
		"gateway":       uiGatewayPayload(),
	})
}

func (s *Server) handleUIOverview(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUIUser(w, r)
	if !ok {
		return
	}

	models := s.uiVisibleModels(user)
	providerIDs, _ := providerIndex(s.cfg, models)
	records := s.ui.snapshotRecordsFor(user.ID)
	summary, series, byModel, byProvider := summarizeUI(records, time.Now())
	recent := records
	if len(recent) > 8 {
		recent = recent[:8]
	}

	writeUIJSON(w, http.StatusOK, map[string]any{
		"gateway": map[string]any{
			"name":           "llm-proxy",
			"version":        Version,
			"started_at":     s.startedAt.UTC(),
			"uptime_seconds": int64(time.Since(s.startedAt).Seconds()),
			"read_only":      true,
			"auth":           "api_key",
		},
		"counts": map[string]any{
			"models":    len(models),
			"providers": len(providerIDs),
		},
		"summary":     summary,
		"series":      series,
		"by_model":    byModel,
		"by_provider": byProvider,
		"recent":      recent,
		"history": map[string]any{
			"size":     len(records),
			"capacity": uiHistorySize,
			"full":     s.ui.historyFull(),
		},
	})
}

func (s *Server) handleUIListModels(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUIUser(w, r)
	if !ok {
		return
	}
	models := s.uiVisibleModels(user)
	payloads := make([]map[string]any, 0, len(models))
	for _, model := range models {
		payloads = append(payloads, s.uiModelPayload(model))
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"models": payloads})
}

func (s *Server) handleUIListProviders(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUIUser(w, r)
	if !ok {
		return
	}
	models := s.uiVisibleModels(user)
	providerIDs, modelsByProvider := providerIndex(s.cfg, models)

	payloads := make([]map[string]any, 0, len(providerIDs))
	for _, id := range providerIDs {
		provider, ok := s.cfg.Providers[id]
		if !ok {
			continue
		}
		payloads = append(payloads, uiProviderPayload(provider, modelsByProvider[id]))
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"providers": payloads})
}

func (s *Server) handleUIRequests(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUIUser(w, r)
	if !ok {
		return
	}

	limit := parseUIInt(r.URL.Query().Get("limit"), uiDefaultLimit, 1, uiMaxLimit)
	offset := parseUIInt(r.URL.Query().Get("offset"), 0, 0, uiHistorySize)
	records := s.ui.snapshotRecordsFor(user.ID)
	total := len(records)
	if offset >= total {
		records = nil
	} else {
		records = records[offset:]
		if len(records) > limit {
			records = records[:limit]
		}
	}
	writeUIJSON(w, http.StatusOK, map[string]any{
		"total":    total,
		"offset":   offset,
		"limit":    limit,
		"capacity": uiHistorySize,
		"records":  records,
	})
}

// requireUIUser enforces the key-derived session on every data endpoint. The
// UI never accepts a raw API key after login, so a stolen page cannot be used
// to enumerate configuration without also stealing the HttpOnly cookie.
func (s *Server) requireUIUser(w http.ResponseWriter, r *http.Request) (*User, time.Time, bool) {
	if !s.cfg.AuthEnabled() {
		writeUIError(w, http.StatusForbidden, uiDisabledMessage, "ui_disabled")
		return nil, time.Time{}, false
	}
	cookie, err := r.Cookie(uiSessionCookie)
	if err != nil || cookie.Value == "" {
		writeUIError(w, http.StatusUnauthorized, "Sign in to continue.", "not_authenticated")
		return nil, time.Time{}, false
	}
	session, ok := s.ui.lookupSession(cookie.Value, time.Now())
	if !ok {
		writeUIError(w, http.StatusUnauthorized, "Your session has expired. Sign in again.", "session_expired")
		return nil, time.Time{}, false
	}
	user, ok := s.usersByID[session.userID]
	if !ok {
		s.ui.deleteSession(cookie.Value)
		writeUIError(w, http.StatusUnauthorized, "Your session has expired. Sign in again.", "session_expired")
		return nil, time.Time{}, false
	}
	return user, session.expiresAt, true
}

func (s *Server) setUISessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     uiSessionCookie,
		Value:    token,
		Path:     uiAPIPath,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   uiRequestIsSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearUISessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     uiSessionCookie,
		Value:    "",
		Path:     uiAPIPath,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   uiRequestIsSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func uiRequestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(proto, "https")
}

func (s *Server) uiVisibleModels(user *User) []*Model {
	models := make([]*Model, 0, len(s.cfg.Models))
	for _, model := range s.cfg.Models {
		if user == nil || user.CanUse(model.ID) {
			models = append(models, model)
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func (s *Server) uiModelPayload(model *Model) map[string]any {
	bindings := make([]map[string]any, 0, len(model.Providers))
	for _, binding := range model.Providers {
		provider, ok := s.cfg.Providers[binding.ProviderID]
		protocols := []ProviderProtocol(nil)
		name := ""
		if ok {
			protocols = provider.Protocols()
			name = provider.Name
		}
		bindings = append(bindings, map[string]any{
			"id":             binding.ProviderID,
			"name":           name,
			"protocols":      protocols,
			"upstream_model": binding.UpstreamModel,
			"weight":         binding.Weight,
		})
	}
	return map[string]any{
		"id":                model.ID,
		"name":              model.Name,
		"modalities":        model.Modalities,
		"output_modalities": model.OutputModalities,
		"providers":         bindings,
		"context_length":    nullable(model.ContextLength),
		"max_output_length": nullable(model.MaxOutputLength),
		"timeout":           nullable(model.Timeout),
	}
}

// nullable renders an optional number as either its value or JSON null.
func nullable[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

func uiProviderPayload(provider *Provider, models []*Model) map[string]any {
	proxyScheme := ""
	if provider.Proxy != "" {
		if parsed, err := url.Parse(provider.Proxy); err == nil {
			proxyScheme = parsed.Scheme
		}
	}
	timeout := any(defaultReadTimeout.Seconds())
	if provider.Timeout != nil {
		timeout = *provider.Timeout
	}
	modelIDs := make([]string, 0, len(models))
	modelNames := make(map[string]string, len(models))
	for _, model := range models {
		modelIDs = append(modelIDs, model.ID)
		if model.Name != "" {
			modelNames[model.ID] = model.Name
		}
	}
	sort.Strings(modelIDs)
	protocols := provider.Protocols()
	baseURLs := make(map[string]string, len(protocols))
	endpoints := make(map[string]map[string]string, len(protocols))
	for _, protocol := range protocols {
		baseURLs[string(protocol)] = displayBaseURL(provider.BaseURLs[protocol])
		byName := make(map[string]string, len(provider.Endpoints[protocol]))
		for _, endpoint := range provider.Endpoints[protocol] {
			byName[endpoint.Name] = endpoint.Path
		}
		endpoints[string(protocol)] = byName
	}
	return map[string]any{
		"id":             provider.ID,
		"name":           provider.Name,
		"protocols":      protocols,
		"base_urls":      baseURLs,
		"endpoints":      endpoints,
		"max_retries":    provider.MaxRetries,
		"timeout":        timeout,
		"has_key":        provider.Key != "",
		"proxy":          provider.Proxy != "",
		"proxy_scheme":   proxyScheme,
		"custom_headers": len(provider.Headers) > 0,
		"models":         modelIDs,
		"model_names":    modelNames,
	}
}

// displayBaseURL hides credentials and query strings that operators may have
// embedded in a provider URL. It is display-only; upstream calls keep the
// configured URL unchanged.
func displayBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// providerIndex groups the visible models by provider in one pass. It returns
// the provider IDs in sorted order plus the models bound to each provider.
func providerIndex(cfg *Config, models []*Model) ([]string, map[string][]*Model) {
	modelsByProvider := make(map[string][]*Model)
	for _, model := range models {
		for _, binding := range model.Providers {
			if _, ok := cfg.Providers[binding.ProviderID]; !ok {
				continue
			}
			modelsByProvider[binding.ProviderID] = append(modelsByProvider[binding.ProviderID], model)
		}
	}
	ids := make([]string, 0, len(modelsByProvider))
	for id := range modelsByProvider {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, modelsByProvider
}

func summarizeUI(records []uiRequestRecord, now time.Time) (uiSummary, []uiBucket, []uiBreakdown, []uiBreakdown) {
	start := now.Truncate(time.Hour).Add(-23 * time.Hour)
	buckets := make([]uiBucket, 24)
	for index := range buckets {
		buckets[index].Start = start.Add(time.Duration(index) * time.Hour)
	}

	summary := uiSummary{Requests: len(records)}
	modelStats := make(map[string]*uiBreakdown)
	providerStats := make(map[string]*uiBreakdown)
	var totalDuration int64

	for _, record := range records {
		if record.Status >= http.StatusBadRequest {
			summary.Errors++
		}
		summary.InputTokens += record.InputTokens
		summary.OutputTokens += record.OutputTokens
		summary.TotalTokens += record.TotalTokens
		totalDuration += record.DurationMS

		if !record.Time.Before(start) {
			index := int(record.Time.Sub(start) / time.Hour)
			if index >= 0 && index < len(buckets) {
				buckets[index].Requests++
				buckets[index].TotalTokens += record.TotalTokens
				if record.Status >= http.StatusBadRequest {
					buckets[index].Errors++
				}
			}
		}

		addBreakdown(modelStats, record.Model, record.ModelName, "No model", record)
		addBreakdown(providerStats, record.Provider, record.ProviderName, "No provider", record)
	}

	summary.Success = summary.Requests - summary.Errors
	if summary.Requests > 0 {
		summary.SuccessRate = float64(summary.Success) / float64(summary.Requests) * 100
		summary.AvgDurationMS = float64(totalDuration) / float64(summary.Requests)
	}
	return summary, buckets, sortedBreakdowns(modelStats), sortedBreakdowns(providerStats)
}

// addBreakdown aggregates by canonical ID while labeling the group with the
// human-readable name, so two entries that share a name stay separate. An empty
// ID falls back to emptyLabel so the row is still identifiable.
func addBreakdown(stats map[string]*uiBreakdown, id, name, emptyLabel string, record uiRequestRecord) {
	if id == "" {
		id = emptyLabel
	}
	if name == "" {
		name = id
	}
	entry, ok := stats[id]
	if !ok {
		entry = &uiBreakdown{Name: name}
		stats[id] = entry
	}
	entry.Requests++
	entry.TotalTokens += record.TotalTokens
	entry.AvgDurationMS += float64(record.DurationMS)
	if record.Status >= http.StatusBadRequest {
		entry.Errors++
	}
}

func sortedBreakdowns(stats map[string]*uiBreakdown) []uiBreakdown {
	out := make([]uiBreakdown, 0, len(stats))
	for _, entry := range stats {
		if entry.Requests > 0 {
			entry.AvgDurationMS /= float64(entry.Requests)
		}
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func uiGatewayPayload() map[string]any {
	return map[string]any{
		"name":    "llm-proxy",
		"version": Version,
	}
}

func (s *Server) userPayload(user *User) map[string]any {
	unrestricted := user.AllowedModels == nil
	for _, allowed := range user.AllowedModels {
		if allowed == "*" {
			unrestricted = true
		}
	}
	allowed := user.AllowedModels
	if allowed == nil {
		allowed = []string{}
	}
	modelNames := make(map[string]string, len(allowed))
	for _, id := range allowed {
		if model, ok := s.cfg.Models[id]; ok && model.Name != "" {
			modelNames[id] = model.Name
		}
	}
	return map[string]any{
		"id":                  user.ID,
		"name":                user.Name,
		"allowed_models":      allowed,
		"allowed_model_names": modelNames,
		"unrestricted":        unrestricted,
	}
}

func parseUIInt(raw string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return min(max(value, minimum), maximum)
}

func writeUIJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("cache-control", uiNoStore)
	w.Header().Set("x-content-type-options", "nosniff")
	writeJSON(w, status, payload)
}

func writeUIError(w http.ResponseWriter, status int, message, code string) {
	writeUIJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"code":    code,
		},
	})
}
