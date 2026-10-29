package anthropic

import (
	"strings"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
)

const minThinkingBudget = 1024

// extraKeys survive a cross-protocol hop. Same-protocol traffic forwards the
// original JSON and does not use Extra.
var extraKeys = map[string]bool{"metadata": true, "thinking": true}

var errorTypeByStatus = map[int]string{
	400: "invalid_request_error",
	401: "authentication_error",
	403: "permission_error",
	404: "not_found_error",
	413: "request_too_large",
	429: "rate_limit_error",
	500: "api_error",
	502: "api_error",
	503: "overloaded_error",
	504: "api_error",
}

var errorTypeAliases = map[string]string{
	"invalid_request_error": "invalid_request_error",
	"permission_error":      "permission_error",
	"authentication_error":  "authentication_error",
	"rate_limit_error":      "rate_limit_error",
	"model_not_found":       "not_found_error",
	"not_found_error":       "not_found_error",
	"overloaded_error":      "overloaded_error",
	"api_error":             "api_error",
	"upstream_error":        "api_error",
}

var deltaTypes = map[string]string{
	"text_delta":       ir.DeltaText,
	"thinking_delta":   ir.DeltaThinking,
	"signature_delta":  ir.DeltaSignature,
	"input_json_delta": ir.DeltaInputJSON,
}

// Messages adapts the Anthropic Messages API.
type Messages struct{}

func parseBlock(raw any) (ir.Block, bool) {
	block, ok := parseBlockPayload(raw)
	if !ok {
		return ir.Block{}, false
	}
	object, ok := util.AsMap(raw)
	if !ok {
		return block, true
	}
	if cache, ok := util.AsMap(object["cache_control"]); ok {
		block.CacheControl = cache
	}
	return block, true
}

func parseBlockPayload(raw any) (ir.Block, bool) {
	if text, ok := raw.(string); ok {
		return ir.TextBlock(text), true
	}
	object, ok := util.AsMap(raw)
	if !ok {
		return ir.Block{}, false
	}

	switch util.StringValue(object["type"]) {
	case "text":
		text, ok := util.AsString(object["text"])
		if !ok {
			return ir.Block{}, false
		}
		return ir.TextBlock(text), true
	case "image":
		source, ok := util.AsMap(object["source"])
		if !ok {
			return ir.Block{}, false
		}
		if util.StringValue(source["type"]) == "url" {
			return ir.Block{Type: ir.Image, ImageURL: util.StringValue(source["url"])}, true
		}
		return ir.Block{
			Type:      ir.Image,
			MediaType: util.StringValue(source["media_type"]),
			Data:      util.StringValue(source["data"]),
		}, true
	case "redacted_thinking":
		return ir.Block{Type: ir.Thinking, Signature: util.StringValue(object["data"])}, true
	case "tool_use":
		input := object["input"]
		if !util.Truthy(input) {
			input = map[string]any{}
		}
		encoded, err := util.Marshal(input)
		if err != nil {
			encoded = []byte("{}")
		}
		return ir.Block{
			Type:      ir.ToolUse,
			ToolUseID: util.StringValue(object["id"]),
			ToolName:  util.StringValue(object["name"]),
			ToolInput: string(encoded),
		}, true
	case "tool_result":
		var text string
		if direct, ok := util.AsString(object["content"]); ok {
			text = direct
		} else {
			blocks := make([]ir.Block, 0)
			if items, ok := util.AsSlice(object["content"]); ok {
				for _, item := range items {
					if block, ok := parseBlock(item); ok {
						blocks = append(blocks, block)
					}
				}
			}
			text = ir.JoinText(blocks)
		}
		return ir.Block{
			Type:      ir.ToolResult,
			Text:      text,
			ToolUseID: util.StringValue(object["tool_use_id"]),
			IsError:   util.Truthy(object["is_error"]),
		}, true
	case "thinking":
		return ir.Block{
			Type:      ir.Thinking,
			Text:      util.StringValue(object["thinking"]),
			Signature: util.StringValue(object["signature"]),
		}, true
	}
	return ir.Block{}, false
}

func parseBlocks(content any) []ir.Block {
	return protocols.BlocksFrom(content, parseBlock)
}

func parseToolChoice(raw any) *string {
	object, ok := util.AsMap(raw)
	if !ok {
		return nil
	}
	switch util.StringValue(object["type"]) {
	case "auto", "none":
		choice := util.StringValue(object["type"])
		return &choice
	case "any":
		choice := "required"
		return &choice
	case "tool":
		name := util.StringValue(object["name"])
		if name == "" {
			return nil
		}
		choice := "tool:" + name
		return &choice
	}
	return nil
}

func formatSystem(blocks []ir.Block) []any {
	hasCache := false
	for _, block := range blocks {
		if block.Type == ir.Text && len(block.CacheControl) > 0 {
			hasCache = true
			break
		}
	}
	if !hasCache {
		return nil
	}
	payload := make([]any, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != ir.Text {
			continue
		}
		entry := map[string]any{"type": "text", "text": block.Text}
		if len(block.CacheControl) > 0 {
			entry["cache_control"] = block.CacheControl
		}
		payload = append(payload, entry)
	}
	return payload
}

func formatBlock(block ir.Block, replay bool) (map[string]any, bool) {
	payload, ok := formatBlockPayload(block, replay)
	if !ok || payload == nil {
		return nil, false
	}
	if len(block.CacheControl) > 0 {
		if _, exists := payload["cache_control"]; !exists {
			payload["cache_control"] = block.CacheControl
		}
	}
	return payload, true
}

func formatBlockPayload(block ir.Block, replay bool) (map[string]any, bool) {
	switch block.Type {
	case ir.Text:
		return map[string]any{"type": "text", "text": block.Text}, true
	case ir.Image:
		if block.Data != "" {
			mediaType := block.MediaType
			if mediaType == "" {
				mediaType = "image/png"
			}
			return map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": mediaType,
					"data":       block.Data,
				},
			}, true
		}
		if block.ImageURL != "" {
			if strings.HasPrefix(block.ImageURL, "data:") {
				header, payload, _ := strings.Cut(block.ImageURL, ",")
				mediaType := strings.TrimPrefix(header, "data:")
				if index := strings.Index(mediaType, ";"); index >= 0 {
					mediaType = mediaType[:index]
				}
				if mediaType == "" {
					mediaType = "image/png"
				}
				return map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": mediaType,
						"data":       payload,
					},
				}, true
			}
			return map[string]any{
				"type":   "image",
				"source": map[string]any{"type": "url", "url": block.ImageURL},
			}, true
		}
		return nil, false
	case ir.ToolUse:
		id := block.ToolUseID
		if id == "" {
			id = "toolu_" + util.CorrelationHex(24)
		}
		input := map[string]any{}
		if block.ToolInput != "" {
			if decoded, err := util.Decode([]byte(block.ToolInput)); err == nil {
				if object, ok := util.AsMap(decoded); ok {
					input = object
				}
			}
		}
		return map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  block.ToolName,
			"input": input,
		}, true
	case ir.ToolResult:
		payload := map[string]any{
			"type":        "tool_result",
			"tool_use_id": block.ToolUseID,
			"content":     block.Text,
		}
		if block.IsError {
			payload["is_error"] = true
		}
		return payload, true
	case ir.Thinking:
		if replay && (block.Signature == "" || ir.IsEncryptedReasoning(block.Signature)) {
			// Anthropic cannot verify a signature it did not mint, and our own
			// encrypted-content carrier is not one either.
			return nil, false
		}
		if block.Text == "" {
			if block.Signature == "" {
				return nil, false
			}
			return map[string]any{"type": "redacted_thinking", "data": block.Signature}, true
		}
		var signature any
		if block.Signature != "" {
			signature = block.Signature
		}
		return map[string]any{
			"type":      "thinking",
			"thinking":  block.Text,
			"signature": signature,
		}, true
	}
	return nil, false
}

func thinkingFromHint(hint ir.ReasoningHint, maxTokens int) map[string]any {
	if hint.Disabled || hint.Effort == "" {
		return nil
	}
	budget, ok := ir.BudgetForEffort(hint.Effort)
	if !ok || maxTokens <= minThinkingBudget {
		return nil
	}
	if budget > maxTokens-1 {
		budget = maxTokens - 1
	}
	return map[string]any{"type": "enabled", "budget_tokens": budget}
}

func streamBlockPayload(block ir.Block) map[string]any {
	switch block.Type {
	case ir.ToolUse:
		id := block.ToolUseID
		if id == "" {
			id = "toolu_" + util.CorrelationHex(24)
		}
		return map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  block.ToolName,
			"input": map[string]any{},
		}
	case ir.Thinking:
		if block.Text == "" && block.Signature != "" && !ir.IsEncryptedReasoning(block.Signature) {
			return map[string]any{"type": "redacted_thinking", "data": block.Signature}
		}
		return map[string]any{
			"type":      "thinking",
			"thinking":  block.Text,
			"signature": block.Signature,
		}
	}
	return map[string]any{"type": "text", "text": block.Text}
}

func formatUsage(usage ir.Usage) map[string]any {
	payload := map[string]any{
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
	}
	if usage.CacheReadInputTokens != 0 {
		payload["cache_read_input_tokens"] = usage.CacheReadInputTokens
	}
	if usage.CacheCreationInputTokens != 0 {
		payload["cache_creation_input_tokens"] = usage.CacheCreationInputTokens
	}
	return payload
}
