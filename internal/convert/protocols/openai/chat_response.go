package openai

import (
	"fmt"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// ParseResponse converts a Chat Completions response into the IR.
func (Chat) ParseResponse(payload map[string]any) (*ir.Response, error) {
	var choice map[string]any
	if choices, ok := util.AsSlice(payload["choices"]); ok && len(choices) > 0 {
		choice, _ = util.AsMap(choices[0])
	}
	message, _ := util.AsMap(choice["message"])

	blocks := make([]ir.Block, 0, 2)
	if reasoning := util.StringValue(message["reasoning_content"]); reasoning != "" {
		blocks = append(blocks, ir.Block{Type: ir.Thinking, Text: reasoning})
	}
	blocks = append(blocks, parseOpenAIContent(message["content"])...)
	if calls, ok := util.AsSlice(message["tool_calls"]); ok {
		for position, call := range calls {
			callMap, ok := util.AsMap(call)
			if !ok {
				continue
			}
			function, _ := util.AsMap(callMap["function"])
			id := util.StringValue(callMap["id"])
			if id == "" {
				id = fmt.Sprintf("call_%d", position)
			}
			arguments := util.StringValue(function["arguments"])
			if arguments == "" {
				arguments = "{}"
			}
			blocks = append(blocks, ir.Block{
				Type:      ir.ToolUse,
				ToolUseID: id,
				ToolName:  util.StringValue(function["name"]),
				ToolInput: arguments,
			})
		}
	}

	stopReason := protocols.ChatFinishToCanonical(util.StringValue(choice["finish_reason"]))
	created := time.Now().Unix()
	if value, ok := util.AsInt(payload["created"]); ok {
		created = int64(value)
	}
	usage, _ := util.AsMap(payload["usage"])

	return &ir.Response{
		ID:         util.StringValue(payload["id"]),
		Model:      util.StringValue(payload["model"]),
		Content:    blocks,
		StopReason: &stopReason,
		Usage:      ir.UsageFrom(usage),
		Created:    created,
	}, nil
}

// FormatResponse converts the IR into a Chat Completions response.
func (Chat) FormatResponse(response *ir.Response, model string) map[string]any {
	segments := SegmentAssistant(response.Content)
	var text strings.Builder
	for _, block := range segments.Visible {
		if block.Type == ir.Text {
			text.WriteString(block.Text)
		}
	}
	toolCalls := make([]any, 0, len(segments.Tools))
	for _, block := range segments.Tools {
		toolCalls = append(toolCalls, toolCallPayload(block, len(toolCalls)))
	}

	message := map[string]any{"role": "assistant"}
	if text.Len() > 0 || len(toolCalls) == 0 {
		message["content"] = text.String()
	} else {
		message["content"] = nil
	}
	if thinking := thinkingText(segments.Thinking); thinking != "" {
		message["reasoning_content"] = thinking
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	id := response.ID
	if id == "" {
		id = "chatcmpl-" + util.CorrelationHex(24)
	}
	created := response.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	stopKey := ir.EndTurn
	if response.StopReason != nil {
		stopKey = *response.StopReason
	}
	finish := protocols.CanonicalToChatFinish(stopKey)

	return map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finish,
			},
		},
		"usage": protocols.FormatUsage(response.Usage, "prompt_tokens", "completion_tokens"),
	}
}

// FormatError renders the OpenAI error envelope.
func (Chat) FormatError(status int, message, errorType, code, requestID string) map[string]any {
	if errorType == "" {
		errorType = util.DefaultErrorType
	}
	var codeValue any
	if code != "" {
		codeValue = code
	}
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
			"code":    codeValue,
			"param":   nil,
		},
	}
}

// RewriteModel replaces the public model name in a Chat Completions payload.
func (Chat) RewriteModel(payload map[string]any, model string) bool {
	return rewriteStringField(payload, "model", model)
}
