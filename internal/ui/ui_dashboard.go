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
	// TTFTMS is the time from request start to the first response byte, which
	// for a stream is effectively time-to-first-token. Zero means nothing was
	// written, so no latency sample exists for this record.
	TTFTMS       int64 `json:"ttft_ms"`
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
	TotalTokens  int   `json:"total_tokens"`
}

type breakdown struct {
	Name          string  `json:"name"`
	Requests      int     `json:"requests"`
	Errors        int     `json:"errors"`
	TotalTokens   int     `json:"total_tokens"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
	AvgTTFTMS     float64 `json:"avg_ttft_ms"`
	AvgOutputTPS  float64 `json:"avg_output_tps"`

	timing latency
}

type requestSummary struct {
	Requests      int     `json:"requests"`
	Errors        int     `json:"errors"`
	Success       int     `json:"success"`
	SuccessRate   float64 `json:"success_rate"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	TotalTokens   int     `json:"total_tokens"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
	AvgTTFTMS     float64 `json:"avg_ttft_ms"`
	AvgOutputTPS  float64 `json:"avg_output_tps"`

	timing latency
}

// latency accumulates streaming latency samples. Time-to-first-token and the
// decode rate only make sense while a reply is generated incrementally: a
// buffered JSON reply arrives as one write, so the gateway records no TTFT for
// it and these accumulators skip records without one.
type latency struct {
	ttftMS    int64
	ttftCount int
	genTokens int
	genTimeMS int64
}

func (t *latency) add(record RequestRecord) {
	if record.Status >= http.StatusBadRequest {
		return
	}
	if record.TTFTMS <= 0 || record.DurationMS <= record.TTFTMS {
		return
	}
	t.ttftMS += record.TTFTMS
	t.ttftCount++
	if record.OutputTokens > 0 {
		t.genTokens += record.OutputTokens
		t.genTimeMS += record.DurationMS - record.TTFTMS
	}
}

// avgTTFT returns the mean time-to-first-token in milliseconds, or zero when
// the window holds no streaming sample.
func (t latency) avgTTFT() float64 {
	if t.ttftCount == 0 {
		return 0
	}
	return float64(t.ttftMS) / float64(t.ttftCount)
}

// outputTPS returns the window's weighted decode throughput: total output
// tokens divided by the time spent streaming them, excluding the wait for the
// first token. Zero means no usable sample.
func (t latency) outputTPS() float64 {
	if t.genTimeMS <= 0 || t.genTokens == 0 {
		return 0
	}
	return float64(t.genTokens) / (float64(t.genTimeMS) / 1000)
}

type seriesBucket struct {
	Start       time.Time `json:"start"`
	Requests    int       `json:"requests"`
	Errors      int       `json:"errors"`
	TotalTokens int       `json:"total_tokens"`
}

// recordRequest appends one completed request to the bounded history window.
func (u *state) recordRequest(record RequestRecord) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.records) < historySize {
		u.records = append(u.records, record)
		return
	}
	u.records[u.next] = record
	u.next = (u.next + 1) % historySize
}

// historyFull reports whether the shared window has started evicting records.
func (u *state) historyFull() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.records) >= historySize
}

// snapshotRecords returns the shared request history newest-first. A non-empty
// userID keeps only that account's records, which is what a member sees; the
// filter runs while collecting, so the caller never copies traffic it may not
// read.
func (u *state) snapshotRecords(userID string) []RequestRecord {
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

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	records := h.recordsFor(user)
	summary, series, byModel, byProvider := summarize(records, time.Now())
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
		"recent":   requestPayloads(recent, user.IsAdmin),
		"history": map[string]any{
			"size":     len(records),
			"capacity": historySize,
			"full":     h.ui.historyFull(),
		},
	}
	if user.IsAdmin {
		payload["by_provider"] = byProvider
	}
	writeJSON(w, http.StatusOK, payload)
}

// recordsFor scopes the in-memory request window to what the caller may see.
// Administrators see every account; members see only their own requests.
func (h *Handler) recordsFor(user *User) []RequestRecord {
	if user.IsAdmin {
		return h.ui.snapshotRecords("")
	}
	return h.ui.snapshotRecords(user.ID)
}

func requestPayloads(records []RequestRecord, includeProvider bool) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, requestPayload(record, includeProvider))
	}
	return out
}

// requestPayload renders one record. Provider names, upstream model IDs and
// provider IDs are administrator-only; members see only their own traffic.
func requestPayload(record RequestRecord, includeProvider bool) map[string]any {
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
		"ttft_ms":       record.TTFTMS,
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

func (h *Handler) handleRequests(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	limit := parseInt(r.URL.Query().Get("limit"), defaultLimit, 1, maxLimit)
	offset := parseInt(r.URL.Query().Get("offset"), 0, 0, historySize)
	records := h.recordsFor(user)
	total := len(records)
	if offset >= total {
		records = nil
	} else {
		records = records[offset:]
		if len(records) > limit {
			records = records[:limit]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":    total,
		"offset":   offset,
		"limit":    limit,
		"capacity": historySize,
		"records":  requestPayloads(records, user.IsAdmin),
	})
}

func summarize(records []RequestRecord, now time.Time) (requestSummary, []seriesBucket, []breakdown, []breakdown) {
	start := now.Truncate(time.Hour).Add(-23 * time.Hour)
	buckets := make([]seriesBucket, 24)
	for index := range buckets {
		buckets[index].Start = start.Add(time.Duration(index) * time.Hour)
	}

	summary := requestSummary{Requests: len(records)}
	modelStats := make(map[string]*breakdown)
	providerStats := make(map[string]*breakdown)
	var totalDuration int64

	for _, record := range records {
		if record.Status >= http.StatusBadRequest {
			summary.Errors++
		}
		summary.InputTokens += record.InputTokens
		summary.OutputTokens += record.OutputTokens
		summary.TotalTokens += record.TotalTokens
		totalDuration += record.DurationMS
		summary.timing.add(record)

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
	summary.AvgTTFTMS = summary.timing.avgTTFT()
	summary.AvgOutputTPS = summary.timing.outputTPS()
	return summary, buckets, sortedBreakdowns(modelStats), sortedBreakdowns(providerStats)
}

// addBreakdown aggregates by canonical ID while labeling the group with the
// human-readable name, so two entries that share a name stay separate. An empty
// ID falls back to emptyLabel so the row is still identifiable.
func addBreakdown(stats map[string]*breakdown, id, name, emptyLabel string, record RequestRecord) {
	if id == "" {
		id = emptyLabel
	}
	if name == "" {
		name = id
	}
	entry, ok := stats[id]
	if !ok {
		entry = &breakdown{Name: name}
		stats[id] = entry
	}
	entry.Requests++
	entry.TotalTokens += record.TotalTokens
	entry.AvgDurationMS += float64(record.DurationMS)
	entry.timing.add(record)
	if record.Status >= http.StatusBadRequest {
		entry.Errors++
	}
}

func sortedBreakdowns(stats map[string]*breakdown) []breakdown {
	out := make([]breakdown, 0, len(stats))
	for _, entry := range stats {
		if entry.Requests > 0 {
			entry.AvgDurationMS /= float64(entry.Requests)
		}
		entry.AvgTTFTMS = entry.timing.avgTTFT()
		entry.AvgOutputTPS = entry.timing.outputTPS()
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
