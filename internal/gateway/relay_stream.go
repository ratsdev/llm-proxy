package gateway

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
)

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
		if len(line) > 0 && (!spec.dropUsageChunk || !isUsageOnlyChunk(line)) {
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
