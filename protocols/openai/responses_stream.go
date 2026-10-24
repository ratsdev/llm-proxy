package openai

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/protocols"
	"github.com/ratsdev/llm-proxy/util"
)

type responsesDecoder struct {
	started         bool
	done            bool
	open            []int
	reasoningSource map[int]string
	toolArguments   map[int]string
	textContent     map[int]string
	stopReason      *string
	usage           ir.Usage
}

// NewDecoder creates a Responses stream decoder.
func (Responses) NewDecoder() protocols.Decoder {
	return &responsesDecoder{
		reasoningSource: map[int]string{},
		toolArguments:   map[int]string{},
		textContent:     map[int]string{},
	}
}

// missingSuffix returns the part of full that has not been streamed yet.
// Providers that only send the terminal ".done" event (and never deltas) rely
// on this to keep their complete text/arguments.
func missingSuffix(seen, full string) string {
	if full == "" || full == seen {
		return ""
	}
	if strings.HasPrefix(full, seen) {
		return full[len(seen):]
	}
	return ""
}

func responseIndex(payload map[string]any, itemType string) int {
	index, _ := util.AsInt(payload["output_index"])
	if itemType == "function_call" {
		return protocols.ToolBlockIndexBase + index
	}
	return index
}

func (d *responsesDecoder) push(index int) {
	for _, existing := range d.open {
		if existing == index {
			return
		}
	}
	d.open = append(d.open, index)
}

func (d *responsesDecoder) reasoningDelta(payload map[string]any, source string) []ir.StreamEvent {
	index := responseIndex(payload, "reasoning")
	seen, exists := d.reasoningSource[index]
	if !exists {
		d.reasoningSource[index] = source
		seen = source
	}
	if seen != source {
		return nil
	}
	delta := util.StringValue(payload["delta"])
	if delta == "" {
		return nil
	}
	return []ir.StreamEvent{{
		Type:      ir.ContentBlockDelta,
		Index:     index,
		DeltaType: ir.DeltaThinking,
		Delta:     delta,
	}}
}

// Feed converts one Responses SSE event into IR events.
func (d *responsesDecoder) Feed(event, data string) ([]ir.StreamEvent, error) {
	if data == "" {
		return nil, nil
	}
	decoded, err := util.Decode([]byte(data))
	if err != nil {
		return nil, nil
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
	case "response.created":
		response, _ := util.AsMap(payload["response"])
		d.started = true
		created := time.Now().Unix()
		if value, ok := util.AsInt(response["created_at"]); ok {
			created = int64(value)
		}
		return []ir.StreamEvent{{
			Type:      ir.MessageStart,
			MessageID: util.StringValue(response["id"]),
			Model:     util.StringValue(response["model"]),
			Created:   created,
		}}, nil
	case "response.output_item.added":
		item, _ := util.AsMap(payload["item"])
		itemType := util.StringValue(item["type"])
		index := responseIndex(payload, itemType)
		var block ir.Block
		switch itemType {
		case "reasoning":
			block = ir.Block{Type: ir.Thinking}
		case "message":
			block = ir.Block{Type: ir.Text}
		case "function_call":
			block = ir.Block{
				Type:      ir.ToolUse,
				ToolUseID: util.StringValue(item["call_id"]),
				ToolName:  util.StringValue(item["name"]),
			}
		default:
			return nil, nil
		}
		d.push(index)
		return []ir.StreamEvent{{Type: ir.ContentBlockStart, Index: index, Block: &block}}, nil
	case "response.output_text.delta":
		delta := util.StringValue(payload["delta"])
		if delta == "" {
			return nil, nil
		}
		index := responseIndex(payload, "message")
		d.textContent[index] += delta
		return []ir.StreamEvent{{
			Type:      ir.ContentBlockDelta,
			Index:     index,
			DeltaType: ir.DeltaText,
			Delta:     delta,
		}}, nil
	case "response.output_text.done":
		full := util.StringValue(payload["text"])
		index := responseIndex(payload, "message")
		suffix := missingSuffix(d.textContent[index], full)
		d.textContent[index] = full
		if suffix == "" {
			return nil, nil
		}
		return []ir.StreamEvent{{
			Type:      ir.ContentBlockDelta,
			Index:     index,
			DeltaType: ir.DeltaText,
			Delta:     suffix,
		}}, nil
	case "response.reasoning_summary_text.delta":
		return d.reasoningDelta(payload, "summary"), nil
	case "response.reasoning_text.delta":
		return d.reasoningDelta(payload, "text"), nil
	case "response.function_call_arguments.delta":
		delta := util.StringValue(payload["delta"])
		if delta == "" {
			return nil, nil
		}
		index := responseIndex(payload, "function_call")
		d.toolArguments[index] += delta
		return []ir.StreamEvent{{
			Type:      ir.ContentBlockDelta,
			Index:     index,
			DeltaType: ir.DeltaInputJSON,
			Delta:     delta,
		}}, nil
	case "response.function_call_arguments.done":
		full := util.StringValue(payload["arguments"])
		index := responseIndex(payload, "function_call")
		suffix := missingSuffix(d.toolArguments[index], full)
		d.toolArguments[index] = full
		if suffix == "" {
			return nil, nil
		}
		return []ir.StreamEvent{{
			Type:      ir.ContentBlockDelta,
			Index:     index,
			DeltaType: ir.DeltaInputJSON,
			Delta:     suffix,
		}}, nil
	case "response.output_item.done":
		item, _ := util.AsMap(payload["item"])
		itemType := util.StringValue(item["type"])
		index := responseIndex(payload, itemType)
		if !slices.Contains(d.open, index) {
			return nil, nil
		}
		d.open = removeInt(d.open, index)
		events := make([]ir.StreamEvent, 0, 2)
		if signature := reasoningSignature(item); itemType == "reasoning" && signature != "" {
			events = append(events, ir.StreamEvent{
				Type:      ir.ContentBlockDelta,
				Index:     index,
				DeltaType: ir.DeltaSignature,
				Delta:     signature,
			})
		}
		events = append(events, ir.StreamEvent{Type: ir.ContentBlockStop, Index: index})
		return events, nil
	case "response.completed", "response.incomplete":
		response, _ := util.AsMap(payload["response"])
		usage, _ := util.AsMap(response["usage"])
		d.usage = protocols.ParseUsage(usage, "input_tokens", "output_tokens")
		content := parseResponseOutput(response["output"])
		stopReason := responseStopReason(response, content)
		d.stopReason = &stopReason
		d.started = true
		d.done = true
		return d.close(), nil
	case "response.failed", "error":
		errorObject, _ := util.AsMap(payload["error"])
		if eventType == "response.failed" {
			if response, ok := util.AsMap(payload["response"]); ok {
				if nested, ok := util.AsMap(response["error"]); ok {
					errorObject = nested
				}
			}
		}
		message := util.StringValue(errorObject["message"])
		if message == "" {
			message = "upstream error"
		}
		errorType := util.StringValue(errorObject["type"])
		if errorType == "" {
			errorType = "api_error"
		}
		d.done = true
		return []ir.StreamEvent{{Type: ir.StreamError, Error: message, ErrorType: errorType}}, nil
	}
	return nil, nil
}

func (d *responsesDecoder) close() []ir.StreamEvent {
	events := make([]ir.StreamEvent, 0, len(d.open)+2)
	for _, index := range d.open {
		events = append(events, ir.StreamEvent{Type: ir.ContentBlockStop, Index: index})
	}
	d.open = nil
	if d.started {
		reason := ir.EndTurn
		if d.stopReason != nil && *d.stopReason != "" {
			reason = *d.stopReason
		}
		usage := d.usage
		events = append(events,
			ir.StreamEvent{Type: ir.MessageDelta, StopReason: &reason, Usage: &usage},
			ir.StreamEvent{Type: ir.MessageStop},
		)
	}
	return events
}

// Finish closes the stream if the upstream never sent a terminal event.
func (d *responsesDecoder) Finish() []ir.StreamEvent {
	if d.done {
		return nil
	}
	d.done = true
	return d.close()
}

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
		e.id = "resp_" + util.RandomHex(24)
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
			itemID := "fc_" + util.RandomHex(24)
			callID := block.ToolUseID
			if callID == "" {
				callID = "call_" + util.RandomHex(12)
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
			itemID := "rs_" + util.RandomHex(24)
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
			itemID := "msg_" + util.RandomHex(24)
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

func removeInt(values []int, target int) []int {
	for index, value := range values {
		if value == target {
			return append(values[:index], values[index+1:]...)
		}
	}
	return values
}
