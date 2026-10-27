package anthropic

import (
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// ParseResponse converts a Messages response into the IR.
func (Messages) ParseResponse(payload map[string]any) (*ir.Response, error) {
	content := make([]ir.Block, 0)
	if items, ok := util.AsSlice(payload["content"]); ok {
		for _, item := range items {
			if block, ok := parseBlock(item); ok {
				content = append(content, block)
			}
		}
	}

	stopReason := protocols.AnthropicStopToCanonical(util.StringValue(payload["stop_reason"]))
	var stopSequence *string
	if text, ok := util.AsString(payload["stop_sequence"]); ok {
		stopSequence = &text
	}

	return &ir.Response{
		ID:           util.StringValue(payload["id"]),
		Model:        util.StringValue(payload["model"]),
		Content:      content,
		StopReason:   &stopReason,
		StopSequence: stopSequence,
		Usage:        ir.UsageFrom(payload["usage"]),
		Created:      time.Now().Unix(),
	}, nil
}

// FormatResponse converts the IR into a Messages response.
func (Messages) FormatResponse(response *ir.Response, model string) map[string]any {
	content := make([]any, 0, len(response.Content))
	for _, block := range response.Content {
		if formatted, ok := formatBlock(block, false); ok {
			content = append(content, formatted)
		}
	}

	id := response.ID
	if id == "" {
		id = "msg_" + util.CorrelationHex(24)
	}
	stopKey := ir.EndTurn
	if response.StopReason != nil {
		stopKey = *response.StopReason
	}
	stopReason := protocols.CanonicalToAnthropicStop(stopKey)
	var stopSequence any
	if response.StopSequence != nil {
		stopSequence = *response.StopSequence
	}

	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": stopSequence,
		"usage":         formatUsage(response.Usage),
	}
}

// FormatError renders the Anthropic error envelope.
func (Messages) FormatError(status int, message, errorType, code, requestID string) map[string]any {
	resolved, ok := errorTypeAliases[errorType]
	if !ok {
		resolved = errorTypeByStatus[status]
		if resolved == "" {
			resolved = "api_error"
		}
	}
	payload := map[string]any{
		"type":  "error",
		"error": map[string]any{"type": resolved, "message": message},
	}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	return payload
}

// RewriteModel replaces the public model name in a Messages payload, which
// carries it both at the top level and inside the message envelope used by
// streaming events.
func (Messages) RewriteModel(payload map[string]any, model string) bool {
	changed := false
	if _, ok := payload["model"].(string); ok {
		payload["model"] = model
		changed = true
	}
	if message, ok := util.AsMap(payload["message"]); ok {
		if _, ok := message["model"].(string); ok {
			message["model"] = model
			changed = true
		}
	}
	return changed
}
