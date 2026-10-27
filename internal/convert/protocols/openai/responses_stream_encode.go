package openai

import (
	"sort"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
)

type responseItem struct {
	kind      string
	id        string
	callID    string
	name      string
	text      string
	arguments string
	signature string
	done      bool
}

type responsesEncoder struct {
	model      string
	id         string
	created    int64
	seq        int
	started    bool
	completed  bool
	slots      protocols.SlotMap
	items      map[int]*responseItem
	stopReason *string
	usage      ir.Usage
}

// NewEncoder creates a Responses stream encoder.
func (Responses) NewEncoder(model string, includeUsage bool) protocols.Encoder {
	return &responsesEncoder{
		model:   model,
		created: time.Now().Unix(),
		items:   map[int]*responseItem{},
	}
}

func (e *responsesEncoder) event(eventType string, payload map[string]any) ([]byte, error) {
	body := map[string]any{"type": eventType, "sequence_number": e.seq}
	for key, value := range payload {
		body[key] = value
	}
	e.seq++
	return util.Frame(eventType, body)
}

func (e *responsesEncoder) start() ([][]byte, error) {
	if e.started {
		return nil, nil
	}
	e.started = true
	if e.id == "" {
		e.id = "resp_" + util.CorrelationHex(24)
	}
	encoded, err := e.event("response.created", map[string]any{
		"response": map[string]any{
			"id":         e.id,
			"object":     "response",
			"created_at": e.created,
			"model":      e.model,
			"status":     "in_progress",
			"output":     []any{},
		},
	})
	if err != nil {
		return nil, err
	}
	return [][]byte{encoded}, nil
}

// Encode converts one IR stream event into Responses SSE frames.
func (e *responsesEncoder) Encode(event ir.StreamEvent) ([][]byte, error) {
	if event.Type == ir.MessageStart {
		if event.MessageID != "" {
			e.id = event.MessageID
		}
		if event.Created != 0 {
			e.created = event.Created
		}
		// Anthropic reports prompt tokens on message_start, while its
		// message_delta only carries output tokens. Keep the earlier value or
		// the final response.completed payload would report zero input tokens.
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
		outputIndex := e.slots.Slot(event.Index)
		switch block.Type {
		case ir.ToolUse:
			itemID := "fc_" + util.CorrelationHex(24)
			callID := block.ToolUseID
			if callID == "" {
				callID = "call_" + util.CorrelationHex(12)
			}
			e.items[outputIndex] = &responseItem{
				kind:   ir.ToolUse,
				id:     itemID,
				callID: callID,
				name:   block.ToolName,
			}
			encoded, err := e.event("response.output_item.added", map[string]any{
				"output_index": outputIndex,
				"item": map[string]any{
					"id":        itemID,
					"type":      "function_call",
					"call_id":   callID,
					"name":      block.ToolName,
					"arguments": "",
					"status":    "in_progress",
				},
			})
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		case ir.Thinking:
			itemID := "rs_" + util.CorrelationHex(24)
			e.items[outputIndex] = &responseItem{
				kind:      ir.Thinking,
				id:        itemID,
				signature: block.Signature,
			}
			added, err := e.event("response.output_item.added", map[string]any{
				"output_index": outputIndex,
				"item": map[string]any{
					"id":      itemID,
					"type":    "reasoning",
					"summary": []any{},
					"status":  "in_progress",
				},
			})
			if err != nil {
				return nil, err
			}
			out = append(out, added)
			partAdded, err := e.event("response.reasoning_summary_part.added", map[string]any{
				"item_id":       itemID,
				"output_index":  outputIndex,
				"summary_index": 0,
				"part":          map[string]any{"type": "summary_text", "text": ""},
			})
			if err != nil {
				return nil, err
			}
			out = append(out, partAdded)
		default:
			itemID := "msg_" + util.CorrelationHex(24)
			e.items[outputIndex] = &responseItem{kind: ir.Text, id: itemID}
			added, err := e.event("response.output_item.added", map[string]any{
				"output_index": outputIndex,
				"item": map[string]any{
					"id":      itemID,
					"type":    "message",
					"role":    "assistant",
					"content": []any{},
					"status":  "in_progress",
				},
			})
			if err != nil {
				return nil, err
			}
			out = append(out, added)
			partAdded, err := e.event("response.content_part.added", map[string]any{
				"item_id":       itemID,
				"output_index":  outputIndex,
				"content_index": 0,
				"part": map[string]any{
					"type":        "output_text",
					"text":        "",
					"annotations": []any{},
				},
			})
			if err != nil {
				return nil, err
			}
			out = append(out, partAdded)
		}
	case ir.ContentBlockDelta:
		outputIndex := e.slots.Slot(event.Index)
		item := e.items[outputIndex]
		if item == nil || event.Delta == "" {
			return out, nil
		}
		switch {
		case event.DeltaType == ir.DeltaText && item.kind == ir.Text:
			item.text += event.Delta
			encoded, err := e.event("response.output_text.delta", map[string]any{
				"item_id":       item.id,
				"output_index":  outputIndex,
				"content_index": 0,
				"delta":         event.Delta,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		case event.DeltaType == ir.DeltaThinking && item.kind == ir.Thinking:
			item.text += event.Delta
			encoded, err := e.event("response.reasoning_summary_text.delta", map[string]any{
				"item_id":       item.id,
				"output_index":  outputIndex,
				"summary_index": 0,
				"delta":         event.Delta,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		case event.DeltaType == ir.DeltaSignature && item.kind == ir.Thinking:
			item.signature = event.Delta
		case event.DeltaType == ir.DeltaInputJSON && item.kind == ir.ToolUse:
			item.arguments += event.Delta
			encoded, err := e.event("response.function_call_arguments.delta", map[string]any{
				"item_id":      item.id,
				"output_index": outputIndex,
				"delta":        event.Delta,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		}
	case ir.ContentBlockStop:
		outputIndex := e.slots.Slot(event.Index)
		if item := e.items[outputIndex]; item != nil {
			doneFrames, err := e.doneEvents(outputIndex, item)
			if err != nil {
				return nil, err
			}
			out = append(out, doneFrames...)
		}
	case ir.MessageDelta:
		e.usage = e.usage.Merged(event.Usage)
		if event.StopReason != nil && *event.StopReason != "" {
			e.stopReason = event.StopReason
		}
	case ir.MessageStop:
		completeFrames, err := e.completeEvents()
		if err != nil {
			return nil, err
		}
		out = append(out, completeFrames...)
	case ir.StreamError:
		if frame := e.errorFrame(event.Error); frame != nil {
			out = append(out, frame)
		}
	}
	return out, nil
}

// errorFrame emits response.failed and marks the response as finished.
func (e *responsesEncoder) errorFrame(message string) []byte {
	e.completed = true
	response := map[string]any{
		"object": "response",
		"status": "failed",
		"output": []any{},
		"error": map[string]any{
			"code":    "server_error",
			"message": message,
		},
	}
	if e.started {
		response["id"] = e.id
		response["created_at"] = e.created
		response["model"] = e.model
		// A started stream must keep the monotonically increasing sequence.
		frame, err := e.event("response.failed", map[string]any{"response": response})
		if err != nil {
			return nil
		}
		return frame
	}
	frame, err := util.Frame("response.failed", map[string]any{
		"type":     "response.failed",
		"response": response,
	})
	if err != nil {
		return nil
	}
	return frame
}

// EncodeStreamError emits response.failed without starting the stream.
func (e *responsesEncoder) EncodeStreamError(message string) [][]byte {
	if frame := e.errorFrame(message); frame != nil {
		return [][]byte{frame}
	}
	return nil
}

func (e *responsesEncoder) doneItem(item *responseItem) map[string]any {
	switch item.kind {
	case ir.Text:
		return responseMessageItem(item.text, item.id)
	case ir.Thinking:
		return reasoningItem(item.text, item.signature, item.id)
	}
	return responseFunctionCallItem(item.callID, item.name, item.arguments, item.id)
}

func (e *responsesEncoder) doneEvents(outputIndex int, item *responseItem) ([][]byte, error) {
	if item.done {
		return nil, nil
	}
	events := make([][]byte, 0, 3)
	switch item.kind {
	case ir.Text:
		encoded, err := e.event("response.output_text.done", map[string]any{
			"item_id":       item.id,
			"output_index":  outputIndex,
			"content_index": 0,
			"text":          item.text,
		})
		if err != nil {
			return nil, err
		}
		events = append(events, encoded)
		part, err := e.event("response.content_part.done", map[string]any{
			"item_id":       item.id,
			"output_index":  outputIndex,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        item.text,
				"annotations": []any{},
			},
		})
		if err != nil {
			return nil, err
		}
		events = append(events, part)
	case ir.Thinking:
		encoded, err := e.event("response.reasoning_summary_text.done", map[string]any{
			"item_id":       item.id,
			"output_index":  outputIndex,
			"summary_index": 0,
			"text":          item.text,
		})
		if err != nil {
			return nil, err
		}
		events = append(events, encoded)
		part, err := e.event("response.reasoning_summary_part.done", map[string]any{
			"item_id":       item.id,
			"output_index":  outputIndex,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": item.text},
		})
		if err != nil {
			return nil, err
		}
		events = append(events, part)
	default:
		encoded, err := e.event("response.function_call_arguments.done", map[string]any{
			"item_id":      item.id,
			"output_index": outputIndex,
			"arguments":    item.arguments,
		})
		if err != nil {
			return nil, err
		}
		events = append(events, encoded)
	}
	done, err := e.event("response.output_item.done", map[string]any{
		"output_index": outputIndex,
		"item":         e.doneItem(item),
	})
	if err != nil {
		return nil, err
	}
	events = append(events, done)
	item.done = true
	return events, nil
}

func (e *responsesEncoder) completeEvents() ([][]byte, error) {
	if e.completed {
		return nil, nil
	}
	e.completed = true
	out, err := e.start()
	if err != nil {
		return nil, err
	}

	indexes := make([]int, 0, len(e.items))
	for index := range e.items {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	for _, index := range indexes {
		item := e.items[index]
		if item.done {
			continue
		}
		doneFrames, err := e.doneEvents(index, item)
		if err != nil {
			return nil, err
		}
		out = append(out, doneFrames...)
	}

	output := make([]any, 0, len(indexes))
	for _, index := range indexes {
		output = append(output, e.doneItem(e.items[index]))
	}

	status, _ := responseStatus(e.stopReason)
	eventType := "response.completed"
	if status == "incomplete" {
		eventType = "response.incomplete"
	}
	payload, err := e.event(eventType, map[string]any{
		"response": responsePayload(e.id, e.model, e.created, output, e.usage, e.stopReason),
	})
	if err != nil {
		return nil, err
	}
	out = append(out, payload)
	return out, nil
}

// Finish completes the response if the stream ended without a terminal event.
func (e *responsesEncoder) Finish() [][]byte {
	if e.completed {
		return nil
	}
	frames, _ := e.completeEvents()
	return frames
}
