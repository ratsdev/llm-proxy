package anthropic

import (
	"fmt"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
)

type streamDecoder struct{}

// NewDecoder creates a Messages stream decoder.
func (Messages) NewDecoder() protocols.Decoder {
	return streamDecoder{}
}

// Feed converts one Messages SSE event into IR events.
func (streamDecoder) Feed(event, data string) ([]ir.StreamEvent, error) {
	if data == "" || event == "ping" || strings.TrimSpace(data) == "[DONE]" {
		return nil, nil
	}
	decoded, err := util.Decode([]byte(data))
	if err != nil {
		// A chunk that is not JSON means the stream is corrupt. Reporting it lets
		// the relay emit a terminal error event instead of closing a truncated
		// reply as if it were complete.
		return nil, fmt.Errorf("messages stream: decode event %q: %w", event, err)
	}
	payload, ok := util.AsMap(decoded)
	if !ok {
		return nil, nil
	}

	eventType := util.StringValue(payload["type"])
	if eventType == "" {
		eventType = event
	}

	switch eventType {
	case "message_start":
		message, _ := util.AsMap(payload["message"])
		return []ir.StreamEvent{{
			Type:      ir.MessageStart,
			MessageID: util.StringValue(message["id"]),
			Model:     util.StringValue(message["model"]),
			Created:   time.Now().Unix(),
			Usage:     usagePtr(ir.UsageFrom(message["usage"])),
		}}, nil
	case "content_block_start":
		index, _ := util.AsInt(payload["index"])
		block, ok := parseBlock(payload["content_block"])
		if !ok {
			block = ir.Block{Type: ir.Text}
		}
		return []ir.StreamEvent{{
			Type:  ir.ContentBlockStart,
			Index: index,
			Block: &block,
		}}, nil
	case "content_block_delta":
		index, _ := util.AsInt(payload["index"])
		delta, _ := util.AsMap(payload["delta"])
		deltaType, ok := deltaTypes[util.StringValue(delta["type"])]
		if !ok {
			return nil, nil
		}
		var text string
		switch deltaType {
		case ir.DeltaText:
			text = util.StringValue(delta["text"])
		case ir.DeltaThinking:
			text = util.StringValue(delta["thinking"])
		case ir.DeltaSignature:
			text = util.StringValue(delta["signature"])
		default:
			text = util.StringValue(delta["partial_json"])
		}
		if text == "" {
			return nil, nil
		}
		return []ir.StreamEvent{{
			Type:      ir.ContentBlockDelta,
			Index:     index,
			DeltaType: deltaType,
			Delta:     text,
		}}, nil
	case "content_block_stop":
		index, _ := util.AsInt(payload["index"])
		return []ir.StreamEvent{{Type: ir.ContentBlockStop, Index: index}}, nil
	case "message_delta":
		delta, _ := util.AsMap(payload["delta"])
		stopReason := protocols.AnthropicStopToCanonical(util.StringValue(delta["stop_reason"]))
		var stopSequence *string
		if text, ok := util.AsString(delta["stop_sequence"]); ok {
			stopSequence = &text
		}
		return []ir.StreamEvent{{
			Type:         ir.MessageDelta,
			StopReason:   &stopReason,
			StopSequence: stopSequence,
			Usage:        usagePtr(ir.UsageFrom(payload["usage"])),
		}}, nil
	case "message_stop":
		return []ir.StreamEvent{{Type: ir.MessageStop}}, nil
	case "error":
		errorObject, ok := util.AsMap(payload["error"])
		if !ok {
			return nil, nil
		}
		message := util.StringValue(errorObject["message"])
		if message == "" {
			message = "upstream error"
		}
		errorType := util.StringValue(errorObject["type"])
		if errorType == "" {
			errorType = "api_error"
		}
		return []ir.StreamEvent{{Type: ir.StreamError, Error: message, ErrorType: errorType}}, nil
	}
	return nil, nil
}

// Finish emits no additional events; Messages streams are self-terminating.
func (streamDecoder) Finish() []ir.StreamEvent {
	return nil
}

type streamEncoder struct {
	model        string
	id           string
	index        protocols.SlotMap
	started      bool
	deltaSent    bool
	stopped      bool
	stopReason   *string
	stopSequence *string
	usage        ir.Usage
}

// NewEncoder creates a Messages stream encoder.
func (Messages) NewEncoder(model string, includeUsage bool) protocols.Encoder {
	return &streamEncoder{model: model}
}

func (e *streamEncoder) start() ([][]byte, error) {
	if e.started {
		return nil, nil
	}
	e.started = true
	if e.id == "" {
		e.id = "msg_" + util.CorrelationHex(24)
	}
	encoded, err := util.Frame("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            e.id,
			"type":          "message",
			"role":          "assistant",
			"model":         e.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         formatUsage(e.usage),
		},
	})
	if err != nil {
		return nil, err
	}
	return [][]byte{encoded}, nil
}

func (e *streamEncoder) messageDelta() ([][]byte, error) {
	if e.deltaSent {
		return nil, nil
	}
	e.deltaSent = true
	stopKey := ir.EndTurn
	if e.stopReason != nil && *e.stopReason != "" {
		stopKey = *e.stopReason
	}
	stopReason := protocols.CanonicalToAnthropicStop(stopKey)
	var stopSequence any
	if e.stopSequence != nil {
		stopSequence = *e.stopSequence
	}
	encoded, err := util.Frame("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": stopSequence,
		},
		"usage": map[string]any{"output_tokens": e.usage.OutputTokens},
	})
	if err != nil {
		return nil, err
	}
	return [][]byte{encoded}, nil
}

// Encode converts one IR stream event into Messages SSE frames.
func (e *streamEncoder) Encode(event ir.StreamEvent) ([][]byte, error) {
	if event.Type == ir.MessageStart {
		if event.MessageID != "" {
			e.id = event.MessageID
		}
		e.usage = e.usage.Merged(event.Usage)
		return e.start()
	}

	out, err := e.start()
	if err != nil {
		return nil, err
	}

	switch event.Type {
	case ir.ContentBlockStart:
		block := ir.Block{Type: ir.Text}
		if event.Block != nil {
			block = *event.Block
		}
		encoded, err := util.Frame("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         e.index.Slot(event.Index),
			"content_block": streamBlockPayload(block),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, encoded)
	case ir.ContentBlockDelta:
		var delta map[string]any
		switch event.DeltaType {
		case ir.DeltaText:
			delta = map[string]any{"type": "text_delta", "text": event.Delta}
		case ir.DeltaThinking:
			delta = map[string]any{"type": "thinking_delta", "thinking": event.Delta}
		case ir.DeltaSignature:
			delta = map[string]any{"type": "signature_delta", "signature": event.Delta}
		case ir.DeltaInputJSON:
			delta = map[string]any{"type": "input_json_delta", "partial_json": event.Delta}
		default:
			return out, nil
		}
		encoded, err := util.Frame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.index.Slot(event.Index),
			"delta": delta,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, encoded)
	case ir.ContentBlockStop:
		if e.index.Has(event.Index) {
			encoded, err := util.Frame("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": e.index.Slot(event.Index),
			})
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		}
	case ir.MessageDelta:
		e.usage = e.usage.Merged(event.Usage)
		if event.StopReason != nil && *event.StopReason != "" {
			e.stopReason = event.StopReason
		}
		e.stopSequence = event.StopSequence
		deltaFrames, err := e.messageDelta()
		if err != nil {
			return nil, err
		}
		out = append(out, deltaFrames...)
	case ir.MessageStop:
		deltaFrames, err := e.messageDelta()
		if err != nil {
			return nil, err
		}
		out = append(out, deltaFrames...)
		if !e.stopped {
			e.stopped = true
			encoded, err := util.Frame("message_stop", map[string]any{"type": "message_stop"})
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		}
	case ir.StreamError:
		if frame := e.errorFrame(event.Error, event.ErrorType); frame != nil {
			out = append(out, frame)
		}
	}
	return out, nil
}

// errorFrame emits the Messages error event and marks the stream as stopped.
func (e *streamEncoder) errorFrame(message, errorType string) []byte {
	if errorType == "" {
		errorType = "api_error"
	}
	e.stopped = true
	frame, err := util.Frame("error", map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errorType,
			"message": message,
		},
	})
	if err != nil {
		return nil
	}
	return frame
}

// EncodeStreamError emits the error event without starting the stream.
func (e *streamEncoder) EncodeStreamError(message string) [][]byte {
	if frame := e.errorFrame(message, "api_error"); frame != nil {
		return [][]byte{frame}
	}
	return nil
}

// Finish emits a terminal message_delta/message_stop when the stream ended
// without one.
func (e *streamEncoder) Finish() [][]byte {
	if e.stopped {
		return nil
	}
	out, _ := e.start()
	deltaFrames, _ := e.messageDelta()
	out = append(out, deltaFrames...)
	encoded, _ := util.Frame("message_stop", map[string]any{"type": "message_stop"})
	out = append(out, encoded)
	e.stopped = true
	return out
}

func usagePtr(usage ir.Usage) *ir.Usage {
	return &usage
}
