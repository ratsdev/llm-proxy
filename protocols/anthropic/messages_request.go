package anthropic

import (
	"strings"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

// ParseRequest converts a Messages body into the IR.
func (Messages) ParseRequest(body map[string]any) (*ir.Request, error) {
	var system []ir.Block
	if text, ok := util.AsString(body["system"]); ok {
		if text != "" {
			system = []ir.Block{ir.TextBlock(text)}
		}
	} else {
		for _, block := range parseBlocks(body["system"]) {
			if block.Type == ir.Text {
				system = append(system, block)
			}
		}
	}

	messages := make([]ir.Message, 0)
	if items, ok := util.AsSlice(body["messages"]); ok {
		for _, item := range items {
			raw, ok := util.AsMap(item)
			if !ok {
				continue
			}
			role := util.StringValue(raw["role"])
			if role != "user" && role != "assistant" {
				continue
			}
			blocks := parseBlocks(raw["content"])
			if len(blocks) == 0 {
				continue
			}
			mapped := ir.RoleUser
			if role == "assistant" {
				mapped = ir.RoleAssistant
			}
			messages = append(messages, ir.Message{Role: mapped, Content: blocks})
		}
	}

	tools := make([]ir.Tool, 0)
	if items, ok := util.AsSlice(body["tools"]); ok {
		for _, item := range items {
			raw, ok := util.AsMap(item)
			if !ok {
				continue
			}
			name := util.StringValue(raw["name"])
			if name == "" {
				continue
			}
			parameters, ok := util.AsMap(raw["input_schema"])
			if !ok {
				parameters = map[string]any{}
			}
			tools = append(tools, ir.Tool{
				Name:        name,
				Description: util.StringValue(raw["description"]),
				Parameters:  parameters,
			})
		}
	}

	stop := make([]string, 0)
	if items, ok := util.AsSlice(body["stop_sequences"]); ok {
		for _, item := range items {
			if text, ok := item.(string); ok {
				stop = append(stop, text)
			}
		}
	}

	extra := make(map[string]any)
	for key, value := range body {
		if extraKeys[key] && value != nil {
			extra[key] = value
		}
	}

	maxTokens, _ := util.AsOptInt(body["max_tokens"])
	temperature, _ := util.AsOptFloat(body["temperature"])
	topP, _ := util.AsOptFloat(body["top_p"])
	topK, _ := util.AsOptInt(body["top_k"])
	stream := util.BoolValue(body["stream"])

	return &ir.Request{
		Messages:     messages,
		System:       system,
		MaxTokens:    maxTokens,
		Temperature:  temperature,
		TopP:         topP,
		TopK:         topK,
		Stop:         stop,
		Tools:        tools,
		ToolChoice:   parseToolChoice(body["tool_choice"]),
		Stream:       stream,
		IncludeUsage: stream,
		Extra:        extra,
	}, nil
}

// FormatRequest converts the IR into a Messages body.
func (Messages) FormatRequest(request *ir.Request, upstreamModel string, maxTokensDefault *int) (map[string]any, error) {
	maxTokens := 4096
	if maxTokensDefault != nil && *maxTokensDefault != 0 {
		maxTokens = *maxTokensDefault
	}
	if request.MaxTokens != nil && *request.MaxTokens != 0 {
		maxTokens = *request.MaxTokens
	}
	out := map[string]any{"model": upstreamModel, "max_tokens": maxTokens}

	if blocks := formatSystem(request.System); blocks != nil {
		out["system"] = blocks
	} else if text := ir.JoinText(request.System); text != "" {
		out["system"] = text
	}

	messages := make([]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		blocks := make([]any, 0, len(message.Content))
		for _, block := range message.Content {
			if formatted, ok := formatBlock(block, true); ok {
				blocks = append(blocks, formatted)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		role := "assistant"
		if message.Role == ir.RoleUser {
			role = "user"
		}
		if len(messages) > 0 {
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] == role {
				// Anthropic requires alternating roles, so fold consecutive
				// turns (e.g. a tool result followed by user text) into one.
				existing, _ := util.AsSlice(last["content"])
				last["content"] = append(existing, blocks...)
				continue
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}
	out["messages"] = messages

	if request.Temperature != nil {
		out["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		out["top_p"] = *request.TopP
	}
	if request.TopK != nil {
		out["top_k"] = *request.TopK
	}
	if len(request.Stop) > 0 {
		out["stop_sequences"] = append([]string(nil), request.Stop...)
	}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			parameters := tool.Parameters
			if len(parameters) == 0 {
				parameters = map[string]any{"type": "object"}
			}
			tools = append(tools, map[string]any{
				"name":         tool.Name,
				"description":  tool.Description,
				"input_schema": parameters,
			})
		}
		out["tools"] = tools
	}
	if request.ToolChoice != nil && *request.ToolChoice != "" {
		choice := *request.ToolChoice
		switch {
		case choice == "required":
			out["tool_choice"] = map[string]any{"type": "any"}
		case strings.HasPrefix(choice, "tool:"):
			out["tool_choice"] = map[string]any{"type": "tool", "name": strings.TrimPrefix(choice, "tool:")}
		default:
			out["tool_choice"] = map[string]any{"type": choice}
		}
	}
	if request.Stream {
		out["stream"] = true
	}

	if _, explicit := request.Extra["thinking"]; !explicit {
		if thinking := thinkingFromHint(ir.ReasoningHintFrom(request.Extra), maxTokens); thinking != nil {
			out["thinking"] = thinking
		}
	}
	for key, value := range request.Extra {
		if extraKeys[key] && value != nil {
			out[key] = value
		}
	}
	return out, nil
}
