package openai

import (
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
)

type chatEncoder struct {
	model        string
	includeUsage bool
	id           string
	created      int64
	started      bool
	finishSent   bool
	done         bool
	toolIndex    protocols.SlotMap
	stopReason   *string
	usage        ir.Usage
}

// NewEncoder creates a Chat Completions stream encoder.
func (Chat) NewEncoder(model string, includeUsage bool) protocols.Encoder {
	return &chatEncoder{
		model:        model,
		includeUsage: includeUsage,
		created:      time.Now().Unix(),
	}
}

func (e *chatEncoder) chunk(delta map[string]any, finishReason any) ([]byte, error) {
	return util.Frame("", map[string]any{
		"id":      e.id,
		"object":  "chat.completion.chunk",
		"created": e.created,
		"model":   e.model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	})
}

func (e *chatEncoder) usageChunk() ([]byte, error) {
	return util.Frame("", map[string]any{
		"id":      e.id,
		"object":  "chat.completion.chunk",
		"created": e.created,
		"model":   e.model,
		"choices": []any{},
		"usage":   protocols.FormatUsage(e.usage, "prompt_tokens", "completion_tokens"),
	})
}

func (e *chatEncoder) start() ([][]byte, error) {
	if e.started {
		return nil, nil
	}
	e.started = true
	if e.id == "" {
		e.id = "chatcmpl-" + util.CorrelationHex(24)
	}
	encoded, err := e.chunk(map[string]any{"role": "assistant", "content": ""}, nil)
	if err != nil {
		return nil, err
	}
	return [][]byte{encoded}, nil
}

func (e *chatEncoder) finishChunk() ([]byte, error) {
	reason := ir.EndTurn
	if e.stopReason != nil && *e.stopReason != "" {
		reason = *e.stopReason
	}
	return e.chunk(map[string]any{}, protocols.CanonicalToChatFinish(reason))
}

// Encode converts one IR stream event into Chat Completions SSE frames.
func (e *chatEncoder) Encode(event ir.StreamEvent) ([][]byte, error) {
	if event.Type == ir.MessageStart {
		if event.MessageID != "" {
			e.id = event.MessageID
		}
		if event.Created != 0 {
			e.created = event.Created
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
		if event.Block != nil && event.Block.Type == ir.ToolUse {
			encoded, err := e.chunk(map[string]any{
				"tool_calls": []any{
					map[string]any{
						"index": e.toolIndex.Slot(event.Index),
						"id":    event.Block.ToolUseID,
						"type":  "function",
						"function": map[string]any{
							"name":      event.Block.ToolName,
							"arguments": "",
						},
					},
				},
			}, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		}
	case ir.ContentBlockDelta:
		if event.Delta == "" {
			return out, nil
		}
		var delta map[string]any
		switch event.DeltaType {
		case ir.DeltaText:
			delta = map[string]any{"content": event.Delta}
		case ir.DeltaThinking:
			delta = map[string]any{"reasoning_content": event.Delta}
		case ir.DeltaInputJSON:
			delta = map[string]any{
				"tool_calls": []any{
					map[string]any{
						"index":    e.toolIndex.Slot(event.Index),
						"function": map[string]any{"arguments": event.Delta},
					},
				},
			}
		}
		if delta != nil {
			encoded, err := e.chunk(delta, nil)
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
		if !e.finishSent {
			e.finishSent = true
			encoded, err := e.finishChunk()
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		}
	case ir.MessageStop:
		if e.done {
			return out, nil
		}
		e.done = true
		if !e.finishSent {
			e.finishSent = true
			encoded, err := e.finishChunk()
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		}
		if e.includeUsage {
			encoded, err := e.usageChunk()
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		}
		done, err := util.Frame("", "[DONE]")
		if err != nil {
			return nil, err
		}
		out = append(out, done)
	case ir.StreamError:
		out = append(out, e.errorFrames(event.Error, event.ErrorType)...)
	}
	return out, nil
}

// errorFrames emits the OpenAI stream error envelope followed by the DONE
// marker and marks the encoder as finished.
func (e *chatEncoder) errorFrames(message, errorType string) [][]byte {
	if errorType == "" {
		errorType = "api_error"
	}
	e.finishSent = true
	e.done = true
	frame, err := util.Frame("", map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
		},
	})
	if err != nil {
		return nil
	}
	done, err := util.Frame("", "[DONE]")
	if err != nil {
		return [][]byte{frame}
	}
	return [][]byte{frame, done}
}

// EncodeStreamError emits the error envelope without starting the stream.
func (e *chatEncoder) EncodeStreamError(message string) [][]byte {
	return e.errorFrames(message, "api_error")
}

// Finish emits any terminal frames that have not been written yet.
func (e *chatEncoder) Finish() [][]byte {
	if e.done {
		return nil
	}
	frames, _ := e.Encode(ir.StreamEvent{Type: ir.MessageStop})
	return frames
}
