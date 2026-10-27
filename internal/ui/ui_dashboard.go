package ui

import (
	"net/http"
	"sort"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

// RequestRecord is one completed model request kept in the in-memory window
// that feeds the Web UI dashboard and log view.
type RequestRecord struct {
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

// recordRequest appends one completed request to the bounded history window.
func (u *uiState) recordRequest(record RequestRecord) {
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

// snapshotRecords returns the shared request history newest-first. A non-empty
// userID keeps only that account's records, which is what a member sees; the
// filter runs while collecting, so the caller never copies traffic it may not
// read.
func (u *uiState) snapshotRecords(userID string) []RequestRecord {
	u.mu.Lock()
	defer u.mu.Unlock()

	size := len(u.records)
	out := make([]RequestRecord, 0, size)
	for offset := 0; offset < size; offset++ {
		record := u.records[(u.next+size-offset-1)%size]
		if userID != "" && record.User != userID {
			continue
		}
		out = append(out, record)
	}
	return out
}

func (h *Handler) handleUIOverview(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.requireUIUser(w, r)
	if !ok {
		return
	}

	records := h.uiRecordsFor(user)
	summary, series, byModel, byProvider := summarizeUI(records, time.Now())
	recent := records
	if len(recent) > 8 {
		recent = recent[:8]
	}

	cfg := h.config()
	modelCount := 0
	for _, model := range cfg.Models {
		if user.CanUse(model.ID) {
			modelCount++
		}
	}
	counts := map[string]any{"models": modelCount}
	if user.IsAdmin {
		counts["providers"] = len(cfg.Providers)
	}

	payload := map[string]any{
		"gateway": map[string]any{
			"name":           "llm-proxy",
			"version":        h.version,
			"started_at":     h.startedAt.UTC(),
			"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
			"converters":     convert.Definitions(),
		},
		"counts":   counts,
		"summary":  summary,
		"series":   series,
		"by_model": byModel,
		"recent":   uiRequestPayloads(recent, user.IsAdmin),
		"history": map[string]any{
			"size":     len(records),
			"capacity": uiHistorySize,
			"full":     h.ui.historyFull(),
		},
	}
	if user.IsAdmin {
		payload["by_provider"] = byProvider
	}
	writeUIJSON(w, http.StatusOK, payload)
}

// uiRecordsFor scopes the in-memory request window to what the caller may see.
// Administrators see every account; members see only their own requests.
func (h *Handler) uiRecordsFor(user *User) []RequestRecord {
	if user.IsAdmin {
		return h.ui.snapshotRecords("")
	}
	return h.ui.snapshotRecords(user.ID)
}

func uiRequestPayloads(records []RequestRecord, includeProvider bool) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, uiRequestPayload(record, includeProvider))
	}
	return out
}

// uiRequestPayload renders one record. Provider names, upstream model IDs and
// provider IDs are administrator-only; members see only their own traffic.
func uiRequestPayload(record RequestRecord, includeProvider bool) map[string]any {
	payload := map[string]any{
		"time":          record.Time,
		"request_id":    record.RequestID,
		"user":          record.User,
		"model":         record.Model,
		"model_name":    record.ModelName,
		"inbound":       record.Inbound,
		"upstream":      record.Upstream,
		"stream":        record.Stream,
		"status":        record.Status,
		"duration_ms":   record.DurationMS,
		"input_tokens":  record.InputTokens,
		"output_tokens": record.OutputTokens,
		"total_tokens":  record.TotalTokens,
	}
	if includeProvider {
		payload["provider"] = record.Provider
		payload["provider_name"] = record.ProviderName
		payload["upstream_model"] = record.UpstreamModel
	}
	return payload
}

func (h *Handler) handleUIRequests(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.requireUIUser(w, r)
	if !ok {
		return
	}

	limit := parseUIInt(r.URL.Query().Get("limit"), uiDefaultLimit, 1, uiMaxLimit)
	offset := parseUIInt(r.URL.Query().Get("offset"), 0, 0, uiHistorySize)
	records := h.uiRecordsFor(user)
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
		"records":  uiRequestPayloads(records, user.IsAdmin),
	})
}

func summarizeUI(records []RequestRecord, now time.Time) (uiSummary, []uiBucket, []uiBreakdown, []uiBreakdown) {
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
func addBreakdown(stats map[string]*uiBreakdown, id, name, emptyLabel string, record RequestRecord) {
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
