package openai

import (
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/protocols"
	"github.com/ratsdev/llm-proxy/util"
)

type chatDecoder struct {
	started      bool
	textOpen     bool
	thinkingOpen bool
	toolOpen     map[int]bool
	openOrder    []int
	toolByID     map[string]int
	toolByName   map[string]int
	toolIndexes  map[int]bool
	nextImplicit int
	lastTool     int
	hasTool      bool
	stopReason   *string
	usage        ir.Usage
}

func (d *chatDecoder) open(index int) {
	for _, existing := range d.openOrder {
		if existing == index {
			return
		}
	}
	d.openOrder = append(d.openOrder, index)
}

// resolveToolIndex maps a streamed tool call onto a stable IR index. OpenAI
// sends an explicit "index", but several compatible providers omit it; those
// calls must be tracked by id/name so distinct calls never merge.
func (d *chatDecoder) resolveToolIndex(call map[string]any, name string) int {
	if index, ok := util.AsInt(call["index"]); ok {
		d.toolIndexes[index] = true
		if id := util.StringValue(call["id"]); id != "" {
			d.toolByID[id] = index
		}
		if name != "" {
			d.toolByName[name] = index
		}
		return index
	}
	if id := util.StringValue(call["id"]); id != "" {
		if index, ok := d.toolByID[id]; ok {
			return index
		}
		index := d.allocateToolIndex()
		d.toolByID[id] = index
		if name != "" {
			d.toolByName[name] = index
		}
		return index
	}
	if name != "" {
		if index, ok := d.toolByName[name]; ok {
			return index
		}
		index := d.allocateToolIndex()
		d.toolByName[name] = index
		return index
	}
	if d.hasTool {
		return d.lastTool
	}
	return d.allocateToolIndex()
}

func (d *chatDecoder) allocateToolIndex() int {
	for d.toolIndexes[d.nextImplicit] {
		d.nextImplicit++
	}
	index := d.nextImplicit
	d.toolIndexes[index] = true
	d.nextImplicit++
	d.lastTool = index
	d.hasTool = true
	return index
}

// Feed converts one Chat Completions SSE chunk into IR events.
func (d *chatDecoder) Feed(event, data string) ([]ir.StreamEvent, error) {
	if data == "" || strings.TrimSpace(data) == "[DONE]" {
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
	if raw, exists := payload["error"]; exists && util.Truthy(raw) {
		object, ok := util.AsMap(raw)
		if !ok {
			return nil, nil
		}
		message := util.StringValue(object["message"])
		if message == "" {
			message = "upstream error"
		}
		errorType := util.StringValue(object["type"])
		if errorType == "" {
			errorType = "api_error"
		}
		return []ir.StreamEvent{{Type: ir.StreamError, Error: message, ErrorType: errorType}}, nil
	}

	events := make([]ir.StreamEvent, 0, 4)
	if !d.started {
		d.started = true
		created := time.Now().Unix()
		if value, ok := util.AsInt(payload["created"]); ok {
			created = int64(value)
		}
		events = append(events, ir.StreamEvent{
			Type:      ir.MessageStart,
			MessageID: util.StringValue(payload["id"]),
			Model:     util.StringValue(payload["model"]),
			Created:   created,
		})
	}

	if usage, ok := util.AsMap(payload["usage"]); ok && len(usage) > 0 {
		d.usage = protocols.ParseUsage(usage, "prompt_tokens", "completion_tokens")
	}

	choices, _ := util.AsSlice(payload["choices"])
	for _, item := range choices {
		choice, ok := util.AsMap(item)
		if !ok {
			continue
		}
		delta, _ := util.AsMap(choice["delta"])
		if delta == nil {
			delta = map[string]any{}
		}

		if reasoning := util.StringValue(delta["reasoning_content"]); reasoning != "" {
			if !d.thinkingOpen {
				d.thinkingOpen = true
				d.open(thinkingBlockIndex)
				events = append(events, ir.StreamEvent{
					Type:  ir.ContentBlockStart,
					Index: thinkingBlockIndex,
					Block: &ir.Block{Type: ir.Thinking},
				})
			}
			events = append(events, ir.StreamEvent{
				Type:      ir.ContentBlockDelta,
				Index:     thinkingBlockIndex,
				DeltaType: ir.DeltaThinking,
				Delta:     reasoning,
			})
		}

		if content := util.StringValue(delta["content"]); content != "" {
			if !d.textOpen {
				d.textOpen = true
				d.open(textBlockIndex)
				events = append(events, ir.StreamEvent{
					Type:  ir.ContentBlockStart,
					Index: textBlockIndex,
					Block: &ir.Block{Type: ir.Text},
				})
			}
			events = append(events, ir.StreamEvent{
				Type:      ir.ContentBlockDelta,
				Index:     textBlockIndex,
				DeltaType: ir.DeltaText,
				Delta:     content,
			})
		}

		if calls, ok := util.AsSlice(delta["tool_calls"]); ok {
			for _, call := range calls {
				callMap, ok := util.AsMap(call)
				if !ok {
					continue
				}
				function, _ := util.AsMap(callMap["function"])
				rawIndex := d.resolveToolIndex(callMap, util.StringValue(function["name"]))
				index := protocols.ToolBlockIndexBase + rawIndex
				if !d.toolOpen[index] {
					d.toolOpen[index] = true
					d.lastTool = rawIndex
					d.hasTool = true
					d.open(index)
					events = append(events, ir.StreamEvent{
						Type:  ir.ContentBlockStart,
						Index: index,
						Block: &ir.Block{
							Type:      ir.ToolUse,
							ToolUseID: util.StringValue(callMap["id"]),
							ToolName:  util.StringValue(function["name"]),
						},
					})
				}
				if arguments := util.StringValue(function["arguments"]); arguments != "" {
					events = append(events, ir.StreamEvent{
						Type:      ir.ContentBlockDelta,
						Index:     index,
						DeltaType: ir.DeltaInputJSON,
						Delta:     arguments,
					})
				}
			}
		}

		if finish := util.StringValue(choice["finish_reason"]); finish != "" {
			reason := protocols.ChatFinishToCanonical(finish)
			d.stopReason = &reason
		}
	}
	return events, nil
}

// Finish closes any open blocks and emits the terminal events.
func (d *chatDecoder) Finish() []ir.StreamEvent {
	events := make([]ir.StreamEvent, 0, len(d.openOrder)+2)
	for _, index := range d.openOrder {
		events = append(events, ir.StreamEvent{Type: ir.ContentBlockStop, Index: index})
	}
	d.openOrder = nil
	d.textOpen = false
	d.thinkingOpen = false
	d.toolOpen = map[int]bool{}
	d.toolByID = map[string]int{}
	d.toolByName = map[string]int{}
	d.toolIndexes = map[int]bool{}
	d.nextImplicit = 0
	d.lastTool = 0
	d.hasTool = false

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

// NewDecoder creates a Chat Completions stream decoder.
func (Chat) NewDecoder() protocols.Decoder {
	return &chatDecoder{
		toolOpen:    map[int]bool{},
		toolByID:    map[string]int{},
		toolByName:  map[string]int{},
		toolIndexes: map[int]bool{},
	}
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
		e.id = "chatcmpl-" + util.RandomHex(24)
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
