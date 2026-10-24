package openai

import (
	"strings"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

// ParseRequest converts a Chat Completions body into the IR.
func (Chat) ParseRequest(body map[string]any) (*ir.Request, error) {
	messages := make([]ir.Message, 0)
	system := make([]ir.Block, 0)

	if items, ok := util.AsSlice(body["messages"]); ok {
		for _, item := range items {
			raw, ok := util.AsMap(item)
			if !ok {
				continue
			}
			role := util.StringValue(raw["role"])
			blocks := parseOpenAIContent(raw["content"])

			switch role {
			case "system", "developer":
				for _, block := range blocks {
					if block.Type == ir.Text {
						system = append(system, block)
					}
				}
				continue
			case "tool":
				messages = append(messages, ir.Message{
					Role: ir.RoleUser,
					Content: []ir.Block{{
						Type:      ir.ToolResult,
						Text:      ir.JoinText(blocks),
						ToolUseID: util.StringValue(raw["tool_call_id"]),
					}},
				})
				continue
			case "assistant":
				enriched := make([]ir.Block, 0, len(blocks)+1)
				if reasoning := util.StringValue(raw["reasoning_content"]); reasoning != "" {
					enriched = append(enriched, ir.Block{Type: ir.Thinking, Text: reasoning})
				}
				for _, block := range blocks {
					switch block.Type {
					case ir.Text, ir.Image, ir.Video:
						enriched = append(enriched, block)
					}
				}
				if calls, ok := util.AsSlice(raw["tool_calls"]); ok {
					for _, call := range calls {
						callMap, ok := util.AsMap(call)
						if !ok {
							continue
						}
						function, _ := util.AsMap(callMap["function"])
						arguments := util.StringValue(function["arguments"])
						if arguments == "" {
							arguments = "{}"
						}
						enriched = append(enriched, ir.Block{
							Type:      ir.ToolUse,
							ToolUseID: util.StringValue(callMap["id"]),
							ToolName:  util.StringValue(function["name"]),
							ToolInput: arguments,
						})
					}
				}
				blocks = enriched
			case "user":
				// Keep as-is.
			default:
				continue
			}

			if len(blocks) > 0 {
				messages = append(messages, ir.Message{Role: role, Content: blocks})
			}
		}
	}

	tools := make([]ir.Tool, 0)
	if items, ok := util.AsSlice(body["tools"]); ok {
		for _, item := range items {
			raw, ok := util.AsMap(item)
			if !ok || util.StringValue(raw["type"]) != "function" {
				continue
			}
			function, _ := util.AsMap(raw["function"])
			name := util.StringValue(function["name"])
			if name == "" {
				continue
			}
			parameters, ok := util.AsMap(function["parameters"])
			if !ok {
				parameters = map[string]any{}
			}
			tools = append(tools, ir.Tool{
				Name:        name,
				Description: util.StringValue(function["description"]),
				Parameters:  parameters,
			})
		}
	}

	includeUsage := false
	if options, ok := util.AsMap(body["stream_options"]); ok {
		includeUsage = util.Truthy(options["include_usage"])
	}

	var maxTokens *int
	usedMaxCompletion := false
	if value, ok := util.AsInt(body["max_completion_tokens"]); ok && value != 0 {
		maxTokens = &value
		usedMaxCompletion = true
	} else if value, ok := util.AsInt(body["max_tokens"]); ok {
		maxTokens = &value
	}

	extra := make(map[string]any)
	for key, value := range body {
		if !chatExtraKeys[key] || value == nil {
			continue
		}
		if key == "max_completion_tokens" && !usedMaxCompletion {
			continue
		}
		extra[key] = value
	}

	temperature, _ := util.AsOptFloat(body["temperature"])
	topP, _ := util.AsOptFloat(body["top_p"])
	topK, _ := util.AsOptInt(body["top_k"])

	return &ir.Request{
		Messages:     messages,
		System:       system,
		MaxTokens:    maxTokens,
		Temperature:  temperature,
		TopP:         topP,
		TopK:         topK,
		Stop:         parseStop(body["stop"]),
		Tools:        tools,
		ToolChoice:   parseToolChoice(body["tool_choice"]),
		Stream:       util.BoolValue(body["stream"]),
		IncludeUsage: includeUsage,
		Extra:        extra,
	}, nil
}

// FormatRequest converts the IR into a Chat Completions body.
func (Chat) FormatRequest(request *ir.Request, upstreamModel string, maxTokensDefault *int) (map[string]any, error) {
	out := map[string]any{"model": upstreamModel}
	messages := make([]any, 0, len(request.Messages)+1)

	if text := ir.JoinText(request.System); text != "" {
		messages = append(messages, map[string]any{"role": "system", "content": text})
	}

	for _, message := range request.Messages {
		if message.Role == ir.RoleAssistant {
			segments := SegmentAssistant(message.Content)
			content := contentPayload(segments.Visible)
			thinking := thinkingText(segments.Thinking)
			if content == nil && len(segments.Tools) == 0 && thinking == "" {
				continue
			}
			entry := map[string]any{"role": "assistant", "content": content}
			if thinking != "" {
				entry["reasoning_content"] = thinking
			}
			if len(segments.Tools) > 0 {
				calls := make([]any, 0, len(segments.Tools))
				for position, block := range segments.Tools {
					calls = append(calls, toolCallPayload(block, position))
				}
				entry["tool_calls"] = calls
			}
			messages = append(messages, entry)
			continue
		}

		for _, block := range message.Content {
			if block.Type == ir.ToolResult {
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": block.ToolUseID,
					"content":      block.Text,
				})
			}
		}

		visible := make([]ir.Block, 0, len(message.Content))
		for _, block := range message.Content {
			switch block.Type {
			case ir.Text, ir.Image, ir.Video:
				visible = append(visible, block)
			}
		}
		if len(visible) > 0 {
			content := contentPayload(visible)
			if content == nil {
				content = ""
			} else if parts, ok := content.([]any); ok && len(parts) == 0 {
				content = ""
			}
			messages = append(messages, map[string]any{"role": "user", "content": content})
		}
	}
	out["messages"] = messages

	maxTokens := maxTokensDefault
	if request.MaxTokens != nil && *request.MaxTokens != 0 {
		maxTokens = request.MaxTokens
	}
	if maxTokens != nil && *maxTokens != 0 {
		// OpenAI reasoning models only accept max_completion_tokens, so keep
		// the key the caller used instead of always rewriting it.
		if _, ok := request.Extra["max_completion_tokens"]; ok {
			out["max_completion_tokens"] = *maxTokens
		} else {
			out["max_tokens"] = *maxTokens
		}
	}
	if request.Temperature != nil {
		out["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		out["top_p"] = *request.TopP
	}

	if hint := ir.ReasoningHintFrom(request.Extra); hint.Effort != "" {
		out["reasoning_effort"] = hint.Effort
	}

	if len(request.Stop) > 0 {
		out["stop"] = append([]string(nil), request.Stop...)
	}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			parameters := tool.Parameters
			if len(parameters) == 0 {
				parameters = map[string]any{"type": "object"}
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool.Name,
					"description": tool.Description,
					"parameters":  parameters,
				},
			})
		}
		out["tools"] = tools
	}
	if request.ToolChoice != nil && *request.ToolChoice != "" {
		choice := *request.ToolChoice
		if name, ok := strings.CutPrefix(choice, "tool:"); ok {
			out["tool_choice"] = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": name},
			}
		} else {
			out["tool_choice"] = choice
		}
	}
	if request.Stream {
		out["stream"] = true
		if request.IncludeUsage {
			out["stream_options"] = map[string]any{"include_usage": true}
		}
	}

	if format := responsesTextToChatFormat(request.Extra["text"]); format != nil {
		out["response_format"] = format
	}
	for key, value := range request.Extra {
		if nonChatKeys[key] {
			continue
		}
		if _, exists := out[key]; exists {
			continue
		}
		out[key] = value
	}
	return out, nil
}
