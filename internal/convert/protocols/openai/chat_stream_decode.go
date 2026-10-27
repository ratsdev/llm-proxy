package openai

import (
	"fmt"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
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

// NewDecoder creates a Chat Completions stream decoder.
func (Chat) NewDecoder() protocols.Decoder {
	return &chatDecoder{
		toolOpen:    map[int]bool{},
		toolByID:    map[string]int{},
		toolByName:  map[string]int{},
		toolIndexes: map[int]bool{},
	}
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
		// A chunk that is not JSON means the stream is corrupt. Reporting it lets
		// the relay emit a terminal error event instead of closing a truncated
		// reply as if it were complete.
		return nil, fmt.Errorf("chat completions stream: decode chunk: %w", err)
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
		d.usage = ir.UsageFrom(usage)
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
