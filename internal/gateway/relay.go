package gateway

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
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

// relaySSE forwards upstream SSE bytes verbatim except for the model name
// embedded in JSON data lines. Interruptions still produce the protocol's
// error event so clients are not left with a truncated stream.
func (s *Server) relaySSE(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	encoder := convert.AdapterFor(spec.inbound).NewEncoder(spec.modelID, spec.includeUsage)
	reader := bufio.NewReaderSize(result.Body, 64<<10)
	lineBuffer := make([]byte, 0, 4<<10)
	for {
		chunk, readErr := reader.ReadSlice('\n')
		lineBuffer = append(lineBuffer, chunk...)
		if len(lineBuffer) > util.DefaultMaxSSELineBytes {
			s.logger.Warn("stream line too long", append(spec.log.fields(),
				"limit", util.DefaultMaxSSELineBytes)...)
			writeStreamError(w, encoder, fmt.Sprintf("sse line exceeds %d bytes", util.DefaultMaxSSELineBytes))
			return
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}

		line := lineBuffer
		if rewritten, ok := rewriteStreamModel(line, spec.modelID); ok {
			line = rewritten
		}
		if usage, ok := usageFromStreamLine(line); ok {
			current := Usage{}
			if spec.log.usage != nil {
				current = *spec.log.usage
			}
			merged := current.Merged(&usage)
			spec.log.usage = &merged
		}
		if len(line) > 0 && !(spec.dropUsageChunk && isUsageOnlyChunk(line)) {
			if _, writeErr := writeToClient(w, line); writeErr != nil {
				s.logger.Debug("client disconnected during stream", append(spec.log.fields(), "error", writeErr)...)
				return
			}
			flush(w)
		}
		lineBuffer = lineBuffer[:0]

		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && r.Context().Err() == nil {
				s.logger.Warn("stream interrupted", append(spec.log.fields(), "error", readErr)...)
				writeStreamError(w, encoder, readErr.Error())
			}
			return
		}
	}
}

// errStreamTerminal stops parsing once a protocol-native error event has been
// emitted; appending finish events afterwards would corrupt the stream.
var errStreamTerminal = errors.New("stream already terminated")

// relayConvertSSE decodes the upstream stream into IR events and re-encodes
// them for the caller's protocol.
func (s *Server) relayConvertSSE(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	decoder := convert.AdapterFor(spec.upstream).NewDecoder()
	encoder := convert.AdapterFor(spec.inbound).NewEncoder(spec.modelID, spec.includeUsage)

	trackUsage := func(usage *Usage) {
		if usage == nil {
			return
		}
		current := Usage{}
		if spec.log.usage != nil {
			current = *spec.log.usage
		}
		merged := current.Merged(usage)
		spec.log.usage = &merged
	}

	errored := false
	parseErr := util.Parse(result.Body, util.DefaultMaxSSELineBytes, func(event, data string) error {
		events, feedErr := decoder.Feed(event, data)
		if feedErr != nil {
			return feedErr
		}
		for _, streamEvent := range events {
			trackUsage(streamEvent.Usage)
			terminal := streamEvent.Type == ir.StreamError
			frames, encodeErr := encoder.Encode(streamEvent)
			if encodeErr != nil {
				return encodeErr
			}
			if err := writeFrames(w, frames); err != nil {
				return err
			}
			if terminal {
				errored = true
				return errStreamTerminal
			}
		}
		return nil
	})

	if parseErr != nil && !errors.Is(parseErr, errStreamTerminal) {
		switch {
		case errors.Is(parseErr, context.Canceled), r.Context().Err() != nil:
			return
		case errors.Is(parseErr, errClientGone):
			s.logger.Debug("client disconnected during stream", append(spec.log.fields(), "error", parseErr)...)
			return
		default:
			s.logger.Warn("stream interrupted", append(spec.log.fields(), "error", parseErr)...)
			if frames, encodeErr := encoder.Encode(ir.StreamEvent{
				Type:      ir.StreamError,
				Error:     parseErr.Error(),
				ErrorType: "api_error",
			}); encodeErr == nil {
				_ = writeFrames(w, frames)
			}
			errored = true
		}
	}
	if errored {
		return
	}

	for _, event := range decoder.Finish() {
		trackUsage(event.Usage)
		if frames, encodeErr := encoder.Encode(event); encodeErr == nil {
			_ = writeFrames(w, frames)
		}
	}
	_ = writeFrames(w, encoder.Finish())
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

// writeStreamError emits the inbound protocol's stream error event.
func writeStreamError(w http.ResponseWriter, encoder protocols.Encoder, message string) {
	for _, frame := range encoder.EncodeStreamError(message) {
		if len(frame) == 0 {
			continue
		}
		if err := writeFrame(w, frame); err != nil {
			return
		}
	}
}

// rewriteStreamModel rewrites the JSON "model" field of one SSE data line in
// place. The line keeps its original field prefix and line terminator, so a
// same-protocol stream stays byte-identical apart from the model name. Only the
// first data line of a multi-line event is rewritten, which is how every
// supported upstream emits JSON payloads.
func rewriteStreamModel(line []byte, modelID string) ([]byte, bool) {
	payload := line
	suffix := []byte(nil)
	switch {
	case bytes.HasSuffix(payload, []byte("\r\n")):
		payload, suffix = payload[:len(payload)-2], []byte("\r\n")
	case bytes.HasSuffix(payload, []byte("\n")):
		payload, suffix = payload[:len(payload)-1], []byte("\n")
	}
	const prefix = "data:"
	if !bytes.HasPrefix(payload, []byte(prefix)) {
		return nil, false
	}
	rest := payload[len(prefix):]
	spaces := 0
	for spaces < len(rest) && rest[spaces] == ' ' {
		spaces++
	}
	body := rest[spaces:]
	if len(body) == 0 || bytes.Equal(body, []byte("[DONE]")) || !bytes.Contains(body, []byte(`"model"`)) {
		return nil, false
	}
	rewritten, ok := util.ReplaceJSONStringField(body, "model", modelID)
	if !ok {
		return nil, false
	}
	out := make([]byte, 0, len(payload)+len(rewritten)-len(body)+len(suffix))
	out = append(out, prefix...)
	out = append(out, rest[:spaces]...)
	out = append(out, rewritten...)
	out = append(out, suffix...)
	return out, true
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
