package openai

import (
	"strings"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/protocols"
	"github.com/ratsdev/llm-proxy/util"
)

// Request fields that must survive a cross-protocol hop. Same-protocol
// traffic forwards the original JSON and does not use Extra.
var responsesExtraKeys = map[string]bool{
	"reasoning":            true,
	"text":                 true,
	"store":                true,
	"previous_response_id": true,
	"metadata":             true,
	"parallel_tool_calls":  true,
	"truncation":           true,
	"include":              true,
	"service_tier":         true,
}

// Responses adapts the OpenAI Responses API.
type Responses struct{}

func reasoningText(item map[string]any) string {
	var summary []string
	for _, block := range parseOpenAIContent(item["summary"]) {
		if block.Text != "" {
			summary = append(summary, block.Text)
		}
	}
	if joined := strings.Join(summary, "\n\n"); joined != "" {
		return joined
	}
	var content []string
	for _, block := range parseOpenAIContent(item["content"]) {
		if block.Text != "" {
			content = append(content, block.Text)
		}
	}
	return strings.Join(content, "\n\n")
}

func reasoningSignature(item map[string]any) string {
	encrypted := util.StringValue(item["encrypted_content"])
	if encrypted == "" {
		return ""
	}
	return ir.EncodeReasoningSignature(encrypted)
}

func responseStopReason(payload map[string]any, content []ir.Block) string {
	for _, block := range content {
		if block.Type == ir.ToolUse {
			return ir.ToolUseStop
		}
	}
	if util.StringValue(payload["status"]) == "incomplete" {
		reason := ""
		if details, ok := util.AsMap(payload["incomplete_details"]); ok {
			reason = util.StringValue(details["reason"])
		}
		if reason == "content_filter" {
			return ir.Refusal
		}
		return ir.MaxTokens
	}
	return ir.EndTurn
}

func parseResponseOutput(items any) []ir.Block {
	blocks := make([]ir.Block, 0)
	list, ok := util.AsSlice(items)
	if !ok {
		return blocks
	}
	for _, item := range list {
		object, ok := util.AsMap(item)
		if !ok {
			continue
		}
		switch util.StringValue(object["type"]) {
		case "reasoning":
			text := reasoningText(object)
			signature := reasoningSignature(object)
			if text != "" || signature != "" {
				blocks = append(blocks, ir.Block{Type: ir.Thinking, Text: text, Signature: signature})
			}
		case "message":
			blocks = append(blocks, parseOpenAIContent(object["content"])...)
		case "function_call":
			blocks = append(blocks, ir.Block{
				Type:      ir.ToolUse,
				ToolUseID: util.StringValue(object["call_id"]),
				ToolName:  util.StringValue(object["name"]),
				ToolInput: responseArguments(object["arguments"]),
			})
		}
	}
	return blocks
}

func reasoningItem(text, signature, itemID string) map[string]any {
	if itemID == "" {
		itemID = "rs_" + util.RandomHex(24)
	}
	summary := []any{}
	if text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	item := map[string]any{
		"id":      itemID,
		"type":    "reasoning",
		"summary": summary,
		"status":  "completed",
	}
	if ir.IsEncryptedReasoning(signature) {
		item["encrypted_content"] = ir.DecodeReasoningSignature(signature)
	}
	return item
}

func responseMessageItem(text, itemID string) map[string]any {
	if itemID == "" {
		itemID = "msg_" + util.RandomHex(24)
	}
	return map[string]any{
		"id":   itemID,
		"type": "message",
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		},
		"status": "completed",
	}
}

func responseFunctionCallItem(callID, name, arguments, itemID string) map[string]any {
	if itemID == "" {
		itemID = "fc_" + util.RandomHex(24)
	}
	if callID == "" {
		callID = "call_" + util.RandomHex(12)
	}
	if arguments == "" {
		arguments = "{}"
	}
	return map[string]any{
		"id":        itemID,
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
		"status":    "completed",
	}
}

func responseStatus(stopReason *string) (string, map[string]any) {
	reason := ""
	if stopReason != nil {
		reason = *stopReason
	}
	switch reason {
	case ir.MaxTokens:
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	case ir.Refusal:
		return "incomplete", map[string]any{"reason": "content_filter"}
	}
	return "completed", nil
}

func responsePayload(id, model string, created int64, output []any, usage ir.Usage, stopReason *string) map[string]any {
	status, incomplete := responseStatus(stopReason)
	payload := map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": created,
		"model":      model,
		"status":     status,
		"output":     output,
		"usage":      protocols.FormatUsage(usage, "input_tokens", "output_tokens"),
	}
	if incomplete != nil {
		payload["incomplete_details"] = incomplete
	}
	return payload
}
