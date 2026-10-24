package llmproxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/protocols"
	"github.com/ratsdev/llm-proxy/util"
)

var (
	errClientGone     = errors.New("client disconnected")
	errStreamTerminal = errors.New("stream already terminated")
)

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request, spec routeSpec) {
	log := logFrom(r)
	log.enabled = true

	user, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	payload, apiErr := readJSONBody(w, r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}

	modelID, _ := util.AsString(payload["model"])
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		s.fail(w, r, util.New(400, "'model' is required.", "", "model_required", nil))
		return
	}
	model, apiErr := s.resolveModel(modelID, user)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.user = userID(user)
	log.model = modelID
	log.inbound = spec.wire
	log.add("user", userID(user), "model", modelID)

	inbound := adapterFor(spec.wire)
	chatFamily := spec.chatFamily()
	stream, includeUsage := inboundStreamFlags(spec.wire, payload)
	routeRequest := RouteRequest{
		Model:           model,
		Operation:       spec.operation,
		Stream:          stream,
		ChatFamily:      chatFamily,
		InboundHeaders:  inboundHeaders(r),
		StickyID:        requestInfoFrom(r).clientID,
		PreferredFormat: spec.wire,
		Payload:         payload,
	}
	log.stream = routeRequest.Stream
	log.add("stream", routeRequest.Stream)

	result, err := s.router.Send(r.Context(), routeRequest)
	if err != nil {
		if r.Context().Err() != nil {
			s.logger.Debug("request canceled", "request_id", requestInfoFrom(r).id, "error", err)
			return
		}
		var gatewayError *util.Error
		if errors.As(err, &gatewayError) {
			s.fail(w, r, gatewayError)
			return
		}
		s.logger.Error("upstream dispatch failed", "request_id", requestInfoFrom(r).id, "error", err)
		s.fail(w, r, util.New(500, "Internal server error.", "api_error", "internal_error", nil))
		return
	}
	log.provider = result.ProviderID
	log.upstreamModel = result.UpstreamModel
	log.upstream = result.WireFormat
	if !result.OK() {
		log.add("provider", result.ProviderID)
		s.fail(w, r, upstreamError(result))
		_ = result.Close()
		return
	}

	w.Header().Set("x-provider-id", result.ProviderID)
	w.Header().Set("x-upstream-model", result.UpstreamModel)
	log.add("provider", result.ProviderID,
		"upstream_model", result.UpstreamModel,
		"upstream_format", result.WireFormat,
		"client_protocol", spec.wire)

	relay := &relaySpec{
		mode:         planMode(chatFamily, routeRequest.Stream, result.WireFormat == spec.wire, isJSONResponse(result)),
		modelID:      modelID,
		inbound:      inbound,
		upstream:     adapterFor(result.WireFormat),
		includeUsage: includeUsage,
		log:          log,
	}
	s.relay(w, r, result, relay)
}

// relayMode selects how an upstream reply is written back to the client.
type relayMode int

const (
	relayRaw relayMode = iota
	relayPassthroughJSON
	relayConvertJSON
	relayPassthroughSSE
	relayConvertSSE
	relayBufferedSSE
)

// planMode decides the write path. Mode selection is pure so it can be tested
// on its own.
func planMode(chatFamily, stream, sameWire, upstreamJSON bool) relayMode {
	switch {
	case !chatFamily:
		return relayRaw
	case stream && upstreamJSON:
		// The upstream ignored "stream" and answered with a normal body.
		return relayBufferedSSE
	case stream && sameWire:
		return relayPassthroughSSE
	case stream:
		return relayConvertSSE
	case sameWire:
		return relayPassthroughJSON
	default:
		return relayConvertJSON
	}
}

type relaySpec struct {
	mode         relayMode
	modelID      string
	inbound      protocols.Adapter
	upstream     protocols.Adapter
	includeUsage bool
	log          *requestLog
}

// relay writes one upstream reply back to the client and owns the shared
// cleanup for every mode.
func (s *Server) relay(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	defer func() {
		if err := result.Close(); err != nil {
			s.logger.Debug("upstream close failed", append(spec.log.attrs, "error", err)...)
		}
	}()

	switch spec.mode {
	case relayRaw:
		s.relayRaw(w, r, result)
	case relayPassthroughJSON:
		s.relayPassthroughJSON(w, r, result, spec)
	case relayConvertJSON:
		s.relayConvertJSON(w, r, result, spec)
	case relayPassthroughSSE:
		startStream(w, result.Status)
		s.relayPassthroughSSE(w, r, result, spec)
	case relayConvertSSE:
		startStream(w, result.Status)
		s.relayConvertSSE(w, r, result, spec)
	case relayBufferedSSE:
		s.relayBufferedSSE(w, r, result, spec)
	}
}

func (s *Server) relayRaw(w http.ResponseWriter, r *http.Request, result *RouteResult) {
	contentType := result.Header.Get("content-type")
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("content-type", contentType)
	w.WriteHeader(result.Status)
	flush(w)

	buffer := make([]byte, 32<<10)
	for {
		n, err := result.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			flush(w)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && r.Context().Err() == nil {
				s.logger.Warn("raw stream interrupted", "error", err)
			}
			return
		}
	}
}

// relayPassthroughJSON forwards a same-protocol upstream body unchanged apart
// from the public model name. It preserves fields the IR does not model
// (logprobs, system_fingerprint, service_tier, ...).
func (s *Server) relayPassthroughJSON(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	data, apiErr := readResult(result)
	if apiErr != nil {
		s.fail(w, r, withProviderHeaders(apiErr, result))
		return
	}
	object, apiErr := decodeObject(data)
	if apiErr != nil {
		s.fail(w, r, withProviderHeaders(apiErr, result))
		return
	}

	spec.inbound.RewriteModel(object, spec.modelID)
	if usage, ok := usageFromObject(object); ok {
		spec.log.usage = &usage
	}
	s.writeRelayJSON(w, r, result, object, spec.log)
}

func (s *Server) relayConvertJSON(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	response, apiErr := s.decodeUpstreamResponse(result, spec.upstream, spec.log)
	if apiErr != nil {
		s.fail(w, r, withProviderHeaders(apiErr, result))
		return
	}
	spec.log.usage = &response.Usage
	s.writeRelayJSON(w, r, result, spec.inbound.FormatResponse(response, spec.modelID), spec.log)
}

// writeRelayJSON encodes and writes a JSON reply, preserving the upstream
// status code.
func (s *Server) writeRelayJSON(w http.ResponseWriter, r *http.Request, result *RouteResult, payload any, log *requestLog) {
	encoded, err := util.Marshal(payload)
	if err != nil {
		s.logger.Error("failed to encode response", append(log.attrs, "error", err)...)
		s.fail(w, r, util.New(500, "Internal server error.", "api_error", "internal_error", nil))
		return
	}

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(result.Status)
	if _, writeErr := w.Write(encoded); writeErr != nil {
		s.logger.Debug("response write failed", append(log.attrs, "error", writeErr)...)
	}
}

// relayPassthroughSSE forwards upstream SSE bytes verbatim except for the
// model name embedded in JSON data lines. Interruptions still produce the
// protocol's error event so clients are not left with a truncated stream.
func (s *Server) relayPassthroughSSE(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	encoder := spec.inbound.NewEncoder(spec.modelID, false)
	reader := bufio.NewReaderSize(result.Body, 64<<10)
	lineBuffer := make([]byte, 0, 4<<10)
	for {
		chunk, readErr := reader.ReadSlice('\n')
		lineBuffer = append(lineBuffer, chunk...)
		if len(lineBuffer) > util.DefaultMaxSSELineBytes {
			s.logger.Warn("stream line too long", append(spec.log.attrs,
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
			current := ir.Usage{}
			if spec.log.usage != nil {
				current = *spec.log.usage
			}
			merged := current.Merged(&usage)
			spec.log.usage = &merged
		}
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				s.logger.Debug("client disconnected during stream", append(spec.log.attrs, "error", writeErr)...)
				return
			}
			flush(w)
		}
		lineBuffer = lineBuffer[:0]

		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && r.Context().Err() == nil {
				s.logger.Warn("stream interrupted", append(spec.log.attrs, "error", readErr)...)
				writeStreamError(w, encoder, readErr.Error())
			}
			return
		}
	}
}

// relayConvertSSE decodes the upstream stream into IR events and re-encodes
// them for the caller's protocol.
func (s *Server) relayConvertSSE(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	decoder := spec.upstream.NewDecoder()
	encoder := spec.inbound.NewEncoder(spec.modelID, spec.includeUsage)

	// Once the stream has produced an error event, the error frame is terminal
	// for every protocol. Emitting decoder/encoder Finish frames afterwards
	// would append message_delta/message_stop or a finish chunk after the
	// error, which clients treat as protocol corruption.
	errored := false
	parseErr := util.Parse(result.Body, util.DefaultMaxSSELineBytes, func(event, data string) error {
		events, feedErr := decoder.Feed(event, data)
		if feedErr != nil {
			return feedErr
		}
		for _, streamEvent := range events {
			if streamEvent.Usage != nil {
				current := ir.Usage{}
				if spec.log.usage != nil {
					current = *spec.log.usage
				}
				merged := current.Merged(streamEvent.Usage)
				spec.log.usage = &merged
			}
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
			s.logger.Debug("client disconnected during stream", append(spec.log.attrs, "error", parseErr)...)
			return
		default:
			s.logger.Warn("stream interrupted", append(spec.log.attrs, "error", parseErr)...)
			frames, encodeErr := encoder.Encode(ir.StreamEvent{
				Type:      ir.StreamError,
				Error:     parseErr.Error(),
				ErrorType: "api_error",
			})
			if encodeErr == nil {
				_ = writeFrames(w, frames)
			}
			errored = true
		}
	}

	if errored {
		return
	}

	for _, event := range decoder.Finish() {
		if frames, encodeErr := encoder.Encode(event); encodeErr == nil {
			_ = writeFrames(w, frames)
		}
	}
	_ = writeFrames(w, encoder.Finish())
}

// relayBufferedSSE turns a complete upstream JSON response into protocol
// stream events. It is used when a client asked for a stream but the upstream
// ignored that request and answered with an ordinary JSON body.
func (s *Server) relayBufferedSSE(w http.ResponseWriter, r *http.Request, result *RouteResult, spec *relaySpec) {
	response, apiErr := s.decodeUpstreamResponse(result, spec.upstream, spec.log)
	if apiErr != nil {
		s.fail(w, r, withProviderHeaders(apiErr, result))
		return
	}

	startStream(w, result.Status)

	encoder := spec.inbound.NewEncoder(spec.modelID, spec.includeUsage)
	for _, event := range response.StreamEvents() {
		frames, encodeErr := encoder.Encode(event)
		if encodeErr != nil {
			s.logger.Warn("stream encode failed", append(spec.log.attrs, "error", encodeErr)...)
			return
		}
		if err := writeFrames(w, frames); err != nil {
			s.logger.Debug("client disconnected during stream", append(spec.log.attrs, "error", err)...)
			return
		}
	}
	if err := writeFrames(w, encoder.Finish()); err != nil {
		s.logger.Debug("client disconnected during stream", append(spec.log.attrs, "error", err)...)
	}
}

// decodeUpstreamResponse buffers and parses a complete upstream body into the
// IR, mapping every failure to a gateway error.
func (s *Server) decodeUpstreamResponse(result *RouteResult, adapter protocols.Adapter, log *requestLog) (*ir.Response, *util.Error) {
	data, apiErr := readResult(result)
	if apiErr != nil {
		return nil, apiErr
	}
	object, apiErr := parseUpstreamObject(data)
	if apiErr != nil {
		return nil, apiErr
	}
	response, err := adapter.ParseResponse(object)
	if err != nil {
		s.logger.Error("failed to parse upstream response", append(log.attrs, "error", err)...)
		return nil, util.New(502,
			"Upstream returned an unsupported response.", "api_error", "upstream_error", nil)
	}
	return response, nil
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

func writeFrames(w http.ResponseWriter, frames [][]byte) error {
	for _, frame := range frames {
		if err := writeFrame(w, frame); err != nil {
			return err
		}
	}
	return nil
}

func writeFrame(w http.ResponseWriter, frame []byte) error {
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("%w: %v", errClientGone, err)
	}
	flush(w)
	return nil
}

// writeStreamError emits the inbound protocol's stream error event.
func writeStreamError(w http.ResponseWriter, encoder protocols.Encoder, message string) {
	for _, frame := range encoder.EncodeStreamError(message) {
		if err := writeFrame(w, frame); err != nil {
			return
		}
	}
}

// rewriteStreamModel rewrites JSON "model" string fields in one SSE data line
// without re-encoding the object, so key order and unknown fields stay intact.
func rewriteStreamModel(line []byte, modelID string) ([]byte, bool) {
	text := strings.TrimRight(string(line), "\r\n")
	if !strings.HasPrefix(text, "data:") {
		return nil, false
	}
	payload := strings.TrimPrefix(text, "data:")
	payload = strings.TrimPrefix(payload, " ")
	payload = strings.TrimSpace(payload)
	if payload == "" || payload == "[DONE]" || !strings.Contains(payload, `"model"`) {
		return nil, false
	}
	rewritten, ok := replaceJSONStringField([]byte(payload), "model", modelID)
	if !ok {
		return nil, false
	}
	return []byte("data: " + string(rewritten) + "\n"), true
}

// usageFromObject extracts token accounting from a JSON response. Both OpenAI
// key families and Anthropic's cache fields are understood so same-wire
// passthrough traffic still contributes to the Web UI usage summary.
func usageFromObject(object map[string]any) (ir.Usage, bool) {
	if usage, ok := usageFromPayload(object); ok {
		return usage, true
	}
	for _, nestedKey := range []string{"message", "response"} {
		if nested, ok := util.AsMap(object[nestedKey]); ok {
			if usage, ok := usageFromPayload(nested); ok {
				return usage, true
			}
		}
	}
	return ir.Usage{}, false
}

func usageFromPayload(object map[string]any) (ir.Usage, bool) {
	raw, ok := util.AsMap(object["usage"])
	if !ok || len(raw) == 0 {
		return ir.Usage{}, false
	}
	if _, ok := raw["prompt_tokens"]; ok {
		return protocols.ParseUsage(raw, "prompt_tokens", "completion_tokens"), true
	}
	usage := protocols.ParseUsage(raw, "input_tokens", "output_tokens")
	if cached, ok := util.AsInt(raw["cache_read_input_tokens"]); ok {
		usage.CacheReadInputTokens = cached
	}
	if created, ok := util.AsInt(raw["cache_creation_input_tokens"]); ok {
		usage.CacheCreationInputTokens = created
	}
	return usage, true
}

func usageFromStreamLine(line []byte) (ir.Usage, bool) {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return ir.Usage{}, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if payload == "" || payload == "[DONE]" || !strings.Contains(payload, `"usage"`) {
		return ir.Usage{}, false
	}
	value, err := util.Decode([]byte(payload))
	if err != nil {
		return ir.Usage{}, false
	}
	object, ok := util.AsMap(value)
	if !ok {
		return ir.Usage{}, false
	}
	return usageFromObject(object)
}

func inboundStreamFlags(wire WireFormat, payload map[string]any) (stream, includeUsage bool) {
	stream, _ = util.AsBool(payload["stream"])
	switch wire {
	case wireOpenAI:
		if options, ok := util.AsMap(payload["stream_options"]); ok {
			includeUsage = util.Truthy(options["include_usage"])
		}
	case wireResponses, wireAnthropic:
		includeUsage = stream
	}
	return stream, includeUsage
}

// replaceJSONStringField substitutes every JSON string value for key, leaving
// the rest of the document bytes unchanged.
func replaceJSONStringField(data []byte, key, value string) ([]byte, bool) {
	quoted, err := util.Marshal(value)
	if err != nil {
		return nil, false
	}
	needle := []byte(`"` + key + `"`)
	changed := false
	var out []byte
	i := 0
	for i < len(data) {
		rel := bytes.Index(data[i:], needle)
		if rel < 0 {
			if !changed {
				return nil, false
			}
			return append(out, data[i:]...), true
		}
		rel += i
		j := rel + len(needle)
		for j < len(data) && isJSONSpace(data[j]) {
			j++
		}
		if j >= len(data) || data[j] != ':' {
			if changed {
				out = append(out, data[i:rel+1]...)
			}
			i = rel + 1
			continue
		}
		j++
		for j < len(data) && isJSONSpace(data[j]) {
			j++
		}
		if j >= len(data) || data[j] != '"' {
			if changed {
				out = append(out, data[i:rel+1]...)
			}
			i = rel + 1
			continue
		}
		end, ok := skipJSONString(data, j)
		if !ok {
			return nil, false
		}
		if bytes.Equal(data[j:end], quoted) {
			if changed {
				out = append(out, data[i:end]...)
			}
			i = end
			continue
		}
		if !changed {
			out = append(out, data[:rel]...)
			changed = true
		} else {
			out = append(out, data[i:rel]...)
		}
		out = append(out, data[rel:j]...)
		out = append(out, quoted...)
		i = end
	}
	if !changed {
		return nil, false
	}
	return out, true
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func skipJSONString(data []byte, start int) (int, bool) {
	if start >= len(data) || data[start] != '"' {
		return 0, false
	}
	for i := start + 1; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

func flush(w http.ResponseWriter) {
	_ = http.NewResponseController(w).Flush()
}
