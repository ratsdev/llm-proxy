package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// statusClientClosedRequest is the conventional status for a request whose
// client disconnected before a response was committed.
const statusClientClosedRequest = 499

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type requestInfo struct {
	id string
}

type requestInfoKey struct{}

// requestLog accumulates the access-log fields a handler wants to report. The
// outermost middleware owns the single "request completed" line, so failures
// that never reach the relay are logged with the same shape as successes.
type requestLog struct {
	enabled       bool
	attrs         []any
	usage         *Usage
	requestID     string
	user          string
	model         string
	provider      string
	upstreamModel string
	inbound       WireFormat
	upstream      WireFormat
	stream        bool
	// streamed reports that the relay actually wrote an SSE body. An upstream
	// JSON reply to a streaming request is buffered, so it has no first-token
	// timing even though the client asked for a stream.
	streamed bool
}

type requestLogKey struct{}

func (l *requestLog) add(attrs ...any) {
	l.attrs = append(l.attrs, attrs...)
}

// fields renders the access-log attributes. Handlers only record state on the
// struct, so the log shape cannot drift between the code that sets a field and
// the code that reports it.
func (l *requestLog) fields() []any {
	attrs := make([]any, 0, len(l.attrs)+12)
	attrs = append(attrs, l.attrs...)
	if l.user != "" {
		attrs = append(attrs, "user", l.user)
	}
	if l.model != "" {
		attrs = append(attrs, "model", l.model)
	}
	if l.provider != "" {
		attrs = append(attrs, "provider", l.provider)
	}
	if l.upstreamModel != "" {
		attrs = append(attrs, "upstream_model", l.upstreamModel)
	}
	if l.inbound != "" {
		attrs = append(attrs, "client_format", string(l.inbound))
	}
	if l.upstream != "" {
		attrs = append(attrs, "upstream_format", string(l.upstream))
	}
	if l.model != "" {
		attrs = append(attrs, "stream", l.stream)
	}
	return attrs
}

func logFrom(r *http.Request) *requestLog {
	log, _ := r.Context().Value(requestLogKey{}).(*requestLog)
	if log == nil {
		log = &requestLog{}
	}
	return log
}

func requestInfoFrom(r *http.Request) requestInfo {
	info, _ := r.Context().Value(requestInfoKey{}).(requestInfo)
	if info.id == "" {
		info.id = "req_" + util.CorrelationHex(32)
	}
	return info
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
		// The caller's request id is trusted only as a correlation label. It is
		// echoed back, but never used for routing affinity: a client that mints a
		// unique id per request would evict real conversations from the pin cache.
		id := strings.TrimSpace(r.Header.Get("x-request-id"))
		if !requestIDPattern.MatchString(id) {
			id = "req_" + util.CorrelationHex(32)
		}
		w.Header().Set("x-request-id", id)
		log := logFrom(r)
		log.requestID = id
		log.add("request_id", id)
		ctx := context.WithValue(r.Context(), requestInfoKey{}, requestInfo{id: id})
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
			// Snapshot the first-byte time before the recovery path can write a
			// synthetic error frame: that frame must not become the TTFT sample.
			ttftMS := recorder.timeToFirstByte(started)
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
					frames := convert.StreamErrorFrames(wireForPath(r.URL.Path),
						"Internal server error.")
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
			attrs := requestLog.fields()
			status := responseStatus(recorder, r)
			timing := requestTiming{elapsed: time.Since(started), ttftMS: ttftMS}
			attrs = append(attrs, "status", status, "duration_ms", timing.elapsed.Milliseconds())
			if requestLog.streamed && timing.ttftMS > 0 {
				attrs = append(attrs, "ttft_ms", timing.ttftMS)
			}
			if requestLog.usage != nil {
				// Structured attributes instead of a formatted string: log pipelines
				// can sum and alert on these without parsing prose.
				attrs = append(attrs,
					"input_tokens", requestLog.usage.PromptTokens(),
					"output_tokens", requestLog.usage.OutputTokens,
					"total_tokens", requestLog.usage.TotalTokens())
			}
			if requestLog.model != "" {
				s.recordUIRequest(requestLog, started, timing, status)
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
	// firstWrite is when the handler produced its first response byte. It is
	// the closest observable proxy for time-to-first-token, because relay
	// writes each SSE frame as soon as the upstream emits it.
	firstWrite time.Time
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
	if r.firstWrite.IsZero() && len(data) > 0 {
		r.firstWrite = time.Now()
	}
	return r.ResponseWriter.Write(data)
}

// timeToFirstByte reports how long the handler took to produce its first
// response byte. Zero means nothing was written, so no latency is known and
// the sample must be skipped when aggregating.
func (r *responseRecorder) timeToFirstByte(started time.Time) int64 {
	if r.firstWrite.IsZero() {
		return 0
	}
	return r.firstWrite.Sub(started).Milliseconds()
}

// statusCode reports the status actually sent to the client.
func (r *responseRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// responseStatus reports the status for logs and UI records. Nothing was sent
// when the caller disconnected before the handler committed a response, so such
// an abandoned request must not be counted as a 200.
func responseStatus(recorder *responseRecorder, r *http.Request) int {
	if !recorder.wroteHeader && r.Context().Err() != nil {
		return statusClientClosedRequest
	}
	return recorder.statusCode()
}

// Unwrap lets http.NewResponseController reach the underlying writer.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// requestTiming is the latency sample of one completed request. It is passed as
// a unit so the elapsed time and the first-byte time cannot be swapped.
type requestTiming struct {
	elapsed time.Duration
	ttftMS  int64
}
