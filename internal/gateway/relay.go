package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// relayMode selects how an upstream reply is written back to the client.
type relayMode int

const (
	relayPassthroughJSON relayMode = iota
	relayConvertJSON
	relayPassthroughSSE
	relayConvertSSE
)

// planMode decides the write path from the upstream reply only. The client's
// stream flag never changes the shape: an upstream JSON body is relayed as
// JSON and an upstream SSE body is relayed as SSE. Same-format bodies pass
// through; cross-format bodies are converted.
func planMode(sameWire, upstreamJSON bool) relayMode {
	switch {
	case upstreamJSON && sameWire:
		return relayPassthroughJSON
	case upstreamJSON:
		return relayConvertJSON
	case sameWire:
		return relayPassthroughSSE
	default:
		return relayConvertSSE
	}
}

// relaySpec describes one upstream reply: how to write it back and what the
// access log should record. The wire formats are the same type the routing
// engine uses, so inbound/upstream are the only two needed.
type relaySpec struct {
	mode         relayMode
	modelID      string
	inbound      convert.Format
	upstream     convert.Format
	includeUsage bool
	// dropUsageChunk hides the trailing usage-only chunk that the gateway
	// requested upstream on behalf of the console, so a caller that did not ask
	// for usage still sees the stream it expects.
	dropUsageChunk bool
	log            *requestLog
}

// relay writes one upstream reply back to the client and owns the shared
// cleanup for every mode.
func (s *Server) relay(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	defer func() {
		if err := result.Close(); err != nil {
			s.logger.Debug("upstream close failed", append(spec.log.fields(), "error", err)...)
		}
	}()

	switch spec.mode {
	case relayPassthroughJSON, relayConvertJSON:
		s.relayJSON(w, r, result, spec)
	case relayPassthroughSSE:
		spec.log.streamed = true
		startStream(w, result.Status)
		s.relaySSE(w, r, result, spec)
	case relayConvertSSE:
		spec.log.streamed = true
		startStream(w, result.Status)
		s.relayConvertSSE(w, r, result, spec)
	}
}

// relayJSON forwards a JSON response. Same-format traffic is written back with
// only the public model name rewritten, so field order and unknown fields are
// preserved byte-for-byte; a registered lossless converter is used for
// cross-format pairs.
func (s *Server) relayJSON(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	data, apiErr := readResult(result)
	if apiErr != nil {
		s.fail(w, r, withProviderHeaders(apiErr, result))
		return
	}
	object, apiErr := parseUpstreamObject(data)
	if apiErr != nil {
		s.fail(w, r, withProviderHeaders(apiErr, result))
		return
	}

	if spec.mode == relayConvertJSON {
		converted, err := convert.Response(spec.upstream, spec.inbound, object, spec.modelID)
		if err != nil {
			s.logger.Error("response conversion failed", append(spec.log.fields(), "error", err)...)
			s.fail(w, r, util.New(502, "Upstream response could not be converted.",
				"api_error", "upstream_error", nil))
			return
		}
		if usage, ok := usageFromObject(converted); ok {
			spec.log.usage = &usage
		}
		encoded, err := util.Marshal(converted)
		if err != nil {
			s.logger.Error("failed to encode response", append(spec.log.fields(), "error", err)...)
			s.fail(w, r, util.New(500, "Internal server error.", "api_error", "internal_error", nil))
			return
		}
		s.writeRelayBytes(w, result, encoded)
		return
	}

	// Passthrough still parses the body for usage accounting; the bytes sent
	// to the client are the upstream bytes with only "model" replaced.
	if usage, ok := usageFromObject(object); ok {
		spec.log.usage = &usage
	}
	if rewritten, ok := util.ReplaceJSONStringField(data, "model", spec.modelID); ok {
		data = rewritten
	}
	s.writeRelayBytes(w, result, data)
}

// inboundStreamIncludeUsage reports whether the caller asked for usage in a
// Chat Completions stream. Messages and Responses always carry usage.
func inboundStreamIncludeUsage(wire WireFormat, payload map[string]any) bool {
	stream, _ := util.AsBool(payload["stream"])
	if !stream {
		return false
	}
	if options, ok := util.AsMap(payload["stream_options"]); ok {
		return util.Truthy(options["include_usage"])
	}
	return wire != wireChatCompletions
}

// requestStreamUsage sets stream_options.include_usage on a streamed Chat
// Completions request. Most providers only append their token usage when the
// flag is set, and the console derives output throughput from it. It reports
// whether the flag was added.
func requestStreamUsage(payload map[string]any, wire WireFormat, stream bool) bool {
	if !stream || wire != wireChatCompletions {
		return false
	}
	options, _ := util.AsMap(payload["stream_options"])
	if util.Truthy(options["include_usage"]) {
		return false
	}
	if options == nil {
		options = make(map[string]any, 1)
		payload["stream_options"] = options
	}
	options["include_usage"] = true
	return true
}

// Token accounting: extracted from buffered JSON bodies and from stream lines,
// so both paths feed the Web UI usage summary.

// usageFromObject extracts token accounting from a JSON response. The nested
// message/response envelopes used by streaming events are searched too, so
// passthrough traffic still contributes to the Web UI usage summary.
func usageFromObject(object map[string]any) (Usage, bool) {
	if usage, ok := usagePayload(object); ok {
		return usage, true
	}
	for _, nestedKey := range []string{"message", "response"} {
		if nested, ok := util.AsMap(object[nestedKey]); ok {
			if usage, ok := usagePayload(nested); ok {
				return usage, true
			}
		}
	}
	return Usage{}, false
}

// usagePayload reads the usage object of one response body. The parsing rules
// live in the IR so passthrough and converted traffic cannot disagree.
func usagePayload(object map[string]any) (Usage, bool) {
	raw, ok := util.AsMap(object["usage"])
	if !ok {
		return Usage{}, false
	}
	return ir.ParseUsage(raw)
}

// usageFromStreamLine extracts token accounting from one SSE data line. It
// works on bytes so the relay's hot path does not copy every line into a
// string just to look for the "usage" key.
func usageFromStreamLine(line []byte) (Usage, bool) {
	trimmed := bytes.TrimSpace(line)
	const prefix = "data:"
	if !bytes.HasPrefix(trimmed, []byte(prefix)) {
		return Usage{}, false
	}
	payload := bytes.TrimSpace(trimmed[len(prefix):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) ||
		!bytes.Contains(payload, []byte(`"usage"`)) {
		return Usage{}, false
	}
	value, err := util.Decode(payload)
	if err != nil {
		return Usage{}, false
	}
	object, ok := util.AsMap(value)
	if !ok {
		return Usage{}, false
	}
	return usageFromObject(object)
}

// isUsageOnlyChunk reports whether an SSE data line carries nothing but token
// usage. Chat Completions upstreams append such a chunk after the last content
// delta when stream_options.include_usage is set, and the gateway asks for that
// chunk itself when the console needs throughput numbers.
func isUsageOnlyChunk(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	const prefix = "data:"
	if !bytes.HasPrefix(trimmed, []byte(prefix)) {
		return false
	}
	payload := bytes.TrimSpace(trimmed[len(prefix):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) ||
		!bytes.Contains(payload, []byte(`"usage"`)) {
		return false
	}
	value, err := util.Decode(payload)
	if err != nil {
		return false
	}
	object, ok := util.AsMap(value)
	if !ok {
		return false
	}
	if _, ok := usageFromObject(object); !ok {
		return false
	}
	choices, ok := object["choices"].([]any)
	return !ok || len(choices) == 0
}

// writeRelayBytes writes an upstream JSON reply verbatim, preserving the
// upstream status code and, when the upstream named one, its media type.
func (s *Server) writeRelayBytes(w http.ResponseWriter, result *RouteResult, data []byte) {
	mediaType := result.Header.Get("content-type")
	if mediaType == "" {
		mediaType = "application/json"
	}
	w.Header().Set("content-type", mediaType)
	w.WriteHeader(result.Status)
	if _, writeErr := writeToClient(w, data); writeErr != nil {
		s.logger.Debug("response write failed", "error", writeErr)
	}
}

func writeFrames(w http.ResponseWriter, frames [][]byte) error {
	for _, frame := range frames {
		if err := writeFrame(w, frame); err != nil {
			return err
		}
	}
	return nil
}

func writeFrame(w http.ResponseWriter, frame []byte) error {
	if _, err := writeToClient(w, frame); err != nil {
		return fmt.Errorf("%w: %v", errClientGone, err)
	}
	flush(w)
	return nil
}

func setStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("x-accel-buffering", "no")
	w.Header().Set("content-type", "text/event-stream")
}

// startStream commits the response as an SSE stream before the first frame is
// written, so buffering proxies and clients see the headers immediately.
func startStream(w http.ResponseWriter, status int) {
	setStreamHeaders(w)
	w.WriteHeader(status)
	flush(w)
}

func isJSONResponse(result *RouteResult) bool {
	return strings.Contains(strings.ToLower(result.Header.Get("content-type")), "json")
}

// clientWriteTimeout bounds one write to a client that stopped reading. The
// server deliberately sets no WriteTimeout, because that would also cap
// streaming replies, so each relay write carries its own deadline; without one a
// stalled TCP connection keeps the handler and the upstream socket alive until
// the kernel gives up.
const clientWriteTimeout = 60 * time.Second

// writeToClient writes one chunk with a bounded deadline. Not every
// ResponseWriter supports deadlines (httptest's does not), so a refused deadline
// falls back to a plain write instead of failing the response.
func writeToClient(w http.ResponseWriter, data []byte) (int, error) {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
	written, err := w.Write(data)
	_ = controller.SetWriteDeadline(time.Time{})
	return written, err
}

// flush pushes buffered frames to the client. A writer that cannot flush is not
// an error: the caller still wrote the bytes.
func flush(w http.ResponseWriter) {
	_ = http.NewResponseController(w).Flush()
}
