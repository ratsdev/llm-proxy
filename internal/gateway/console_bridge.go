package gateway

import (
	"net/http"
	"time"

	"github.com/ratsdev/llm-proxy/internal/router"
	"github.com/ratsdev/llm-proxy/internal/ui"
)

// This file is the seam between the gateway and the Web console. Keeping the
// wiring here leaves the HTTP middleware and the route table free of console
// concerns.

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

// recordUIRequest feeds the in-memory dashboard window. Only model requests
// reach this path, so health probes and the Web UI itself stay out of the
// traffic statistics.
func (s *Server) recordUIRequest(log *requestLog, started time.Time, timing requestTiming, status int) {
	record := ui.RequestRecord{
		Time:          started.UTC(),
		RequestID:     log.requestID,
		User:          log.user,
		Model:         log.model,
		ModelName:     s.displayModelName(log.model),
		Provider:      log.provider,
		ProviderName:  s.displayProviderName(log.provider),
		UpstreamModel: log.upstreamModel,
		Inbound:       string(log.inbound),
		Upstream:      string(log.upstream),
		Stream:        log.stream,
		Status:        status,
		DurationMS:    timing.elapsed.Milliseconds(),
	}
	if log.usage != nil {
		record.InputTokens = log.usage.PromptTokens()
		record.OutputTokens = log.usage.OutputTokens
		record.TotalTokens = log.usage.TotalTokens()
	}
	if log.streamed {
		record.TTFTMS = timing.ttftMS
	}
	s.ui.RecordRequest(record)
}

// displayModelName resolves a model id to the operator-facing name.
func (s *Server) displayModelName(id string) string {
	if model, ok := s.cfg.Models[id]; ok && model.Name != "" {
		return model.Name
	}
	return id
}

// displayProviderName resolves a provider id to the operator-facing name.
func (s *Server) displayProviderName(id string) string {
	if provider, ok := s.cfg.Providers[id]; ok && provider.Name != "" {
		return provider.Name
	}
	return id
}
