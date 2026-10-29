package openai

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
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
		return ir.ToolBlockIndexBase + index
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
	if data == "" || strings.TrimSpace(data) == "[DONE]" {
		return nil, nil
	}
	decoded, err := util.Decode([]byte(data))
	if err != nil {
		// A chunk that is not JSON means the stream is corrupt. Reporting it lets
		// the relay emit a terminal error event instead of closing a truncated
		// reply as if it were complete.
		return nil, fmt.Errorf("responses stream: decode event %q: %w", event, err)
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
		d.usage = ir.UsageFrom(usage)
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
