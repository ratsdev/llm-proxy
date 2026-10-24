package openai

import (
	"strings"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

// responseFormatKeys are shared by Chat's response_format.json_schema and the
// Responses API's text.format.
var responseFormatKeys = []string{"name", "schema", "strict", "description"}

func copyFormatKeys(source map[string]any) map[string]any {
	result := make(map[string]any, len(responseFormatKeys))
	for _, key := range responseFormatKeys {
		if value, exists := source[key]; exists {
			result[key] = value
		}
	}
	return result
}

// chatResponseFormatToResponsesFormat translates Chat Completions
// response_format into the Responses API text.format shape.
func chatResponseFormatToResponsesFormat(raw any) map[string]any {
	format, ok := util.AsMap(raw)
	if !ok {
		return nil
	}
	switch util.StringValue(format["type"]) {
	case "json_schema":
		schema, ok := util.AsMap(format["json_schema"])
		if !ok {
			return nil
		}
		result := copyFormatKeys(schema)
		result["type"] = "json_schema"
		return result
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "text":
		return map[string]any{"type": "text"}
	}
	return nil
}

// responsesTextToChatFormat translates a Responses API "text" object into
// Chat Completions response_format.
func responsesTextToChatFormat(raw any) any {
	text, ok := util.AsMap(raw)
	if !ok {
		return nil
	}
	format, ok := util.AsMap(text["format"])
	if !ok {
		return nil
	}
	switch util.StringValue(format["type"]) {
	case "json_schema":
		return map[string]any{"type": "json_schema", "json_schema": copyFormatKeys(format)}
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "text":
		return map[string]any{"type": "text"}
	}
	return nil
}

func parseFunctionOutput(output any) string {
	if text, ok := output.(string); ok {
		return text
	}
	return ir.JoinText(parseOpenAIContent(output))
}

func addMessage(messages *[]ir.Message, role string, blocks []ir.Block) {
	if len(blocks) == 0 {
		return
	}
	if len(*messages) > 0 {
		last := &(*messages)[len(*messages)-1]
		if last.Role == role {
			last.Content = append(last.Content, blocks...)
			return
		}
	}
	*messages = append(*messages, ir.Message{Role: role, Content: blocks})
}

func parseResponseInput(raw any) ([]ir.Block, []ir.Message) {
	system := make([]ir.Block, 0)
	messages := make([]ir.Message, 0)

	if text, ok := raw.(string); ok {
		if text != "" {
			messages = append(messages, ir.Message{Role: ir.RoleUser, Content: []ir.Block{ir.TextBlock(text)}})
		}
		return system, messages
	}
	items, ok := util.AsSlice(raw)
	if !ok {
		return system, messages
	}

	for _, item := range items {
		if text, ok := item.(string); ok {
			addMessage(&messages, ir.RoleUser, []ir.Block{ir.TextBlock(text)})
			continue
		}
		object, ok := util.AsMap(item)
		if !ok {
			continue
		}

		switch util.StringValue(object["type"]) {
		case "", "message":
			role := util.StringValue(object["role"])
			if role == "" {
				role = ir.RoleUser
			}
			blocks := parseOpenAIContent(object["content"])
			if role == "system" || role == "developer" {
				for _, block := range blocks {
					if block.Type == ir.Text {
						system = append(system, block)
					}
				}
				continue
			}
			normalized := ir.RoleUser
			if role == "assistant" {
				normalized = ir.RoleAssistant
			}
			addMessage(&messages, normalized, blocks)
		case "reasoning":
			text := reasoningText(object)
			signature := reasoningSignature(object)
			if text != "" || signature != "" {
				addMessage(&messages, ir.RoleAssistant, []ir.Block{{
					Type:      ir.Thinking,
					Text:      text,
					Signature: signature,
				}})
			}
		case "function_call":
			addMessage(&messages, ir.RoleAssistant, []ir.Block{{
				Type:      ir.ToolUse,
				ToolUseID: util.StringValue(object["call_id"]),
				ToolName:  util.StringValue(object["name"]),
				ToolInput: responseArguments(object["arguments"]),
			}})
		case "function_call_output":
			addMessage(&messages, ir.RoleUser, []ir.Block{{
				Type:      ir.ToolResult,
				Text:      parseFunctionOutput(object["output"]),
				ToolUseID: util.StringValue(object["call_id"]),
			}})
		}
	}
	return system, messages
}

func responseArguments(value any) string {
	return defaultArguments(util.StringValue(value))
}

// ParseRequest converts a Responses body into the IR.
func (Responses) ParseRequest(body map[string]any) (*ir.Request, error) {
	system, messages := parseResponseInput(body["input"])
	if instructions := util.StringValue(body["instructions"]); instructions != "" {
		system = append([]ir.Block{ir.TextBlock(instructions)}, system...)
	}

	tools := make([]ir.Tool, 0)
	if items, ok := util.AsSlice(body["tools"]); ok {
		for _, item := range items {
			raw, ok := util.AsMap(item)
			if !ok || util.StringValue(raw["type"]) != "function" {
				continue
			}
			function, ok := util.AsMap(raw["function"])
			if !ok {
				function = raw
			}
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

	extra := make(map[string]any)
	for key, value := range body {
		if responsesExtraKeys[key] && value != nil {
			extra[key] = value
		}
	}

	var maxTokens *int
	if value, ok := util.AsInt(body["max_output_tokens"]); ok && value != 0 {
		maxTokens = &value
	} else if value, ok := util.AsInt(body["max_tokens"]); ok {
		maxTokens = &value
	}
	temperature, _ := util.AsOptFloat(body["temperature"])
	topP, _ := util.AsOptFloat(body["top_p"])
	stream := util.BoolValue(body["stream"])

	return &ir.Request{
		Messages:     messages,
		System:       system,
		MaxTokens:    maxTokens,
		Temperature:  temperature,
		TopP:         topP,
		Tools:        tools,
		ToolChoice:   parseToolChoice(body["tool_choice"]),
		Stream:       stream,
		IncludeUsage: stream,
		Extra:        extra,
	}, nil
}

func responseInputPart(block ir.Block) map[string]any {
	switch block.Type {
	case ir.Text:
		return map[string]any{"type": "input_text", "text": block.Text}
	case ir.Image:
		url := imageURL(block)
		if url == "" {
			return nil
		}
		return map[string]any{"type": "input_image", "image_url": url}
	case ir.Video:
		if block.VideoURL == "" {
			return nil
		}
		return map[string]any{"type": "input_video", "video_url": block.VideoURL}
	}
	return nil
}

func reasoningPayload(request *ir.Request) map[string]any {
	hint := ir.ReasoningHintFrom(request.Extra)
	switch {
	case hint.Raw != nil:
		return copyMap(hint.Raw)
	case hint.Effort != "":
		return map[string]any{"effort": hint.Effort}
	case hint.Disabled:
		return map[string]any{"effort": "none"}
	}
	return nil
}

func copyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func assistantItems(message ir.Message) []any {
	items := make([]any, 0, len(message.Content))
	summaries := make([]string, 0)
	textParts := make([]string, 0)

	flush := func() {
		if len(summaries) > 0 {
			parts := make([]any, 0, len(summaries))
			for _, part := range summaries {
				parts = append(parts, map[string]any{"type": "summary_text", "text": part})
			}
			items = append(items, map[string]any{"type": "reasoning", "summary": parts})
			summaries = summaries[:0]
		}
		if text := strings.Join(textParts, ""); text != "" {
			items = append(items, map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "output_text", "text": text},
				},
			})
		}
		textParts = textParts[:0]
	}

	for _, block := range message.Content {
		switch block.Type {
		case ir.Thinking:
			if block.Text == "" && block.Signature == "" {
				continue
			}
			if ir.IsEncryptedReasoning(block.Signature) {
				// One encrypted item per block: merging would lose one.
				flush()
				summary := []any{}
				if block.Text != "" {
					summary = append(summary, map[string]any{"type": "summary_text", "text": block.Text})
				}
				items = append(items, map[string]any{
					"type":              "reasoning",
					"summary":           summary,
					"encrypted_content": ir.DecodeReasoningSignature(block.Signature),
				})
				continue
			}
			if block.Text != "" {
				summaries = append(summaries, block.Text)
			}
		case ir.Text:
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case ir.ToolUse:
			flush()
			callID := block.ToolUseID
			if callID == "" {
				callID = "call_" + util.RandomHex(12)
			}
			items = append(items, map[string]any{
				"type":      "function_call",
				"call_id":   callID,
				"name":      block.ToolName,
				"arguments": defaultArguments(block.ToolInput),
			})
		}
	}
	flush()
	return items
}

func defaultArguments(arguments string) string {
	if arguments == "" {
		return "{}"
	}
	return arguments
}

// FormatRequest converts the IR into a Responses body.
func (Responses) FormatRequest(request *ir.Request, upstreamModel string, maxTokensDefault *int) (map[string]any, error) {
	out := map[string]any{"model": upstreamModel}

	if text := ir.JoinText(request.System); text != "" {
		out["instructions"] = text
	}

	items := make([]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role == ir.RoleAssistant {
			items = append(items, assistantItems(message)...)
			continue
		}
		for _, block := range message.Content {
			if block.Type == ir.ToolResult {
				items = append(items, map[string]any{
					"type":    "function_call_output",
					"call_id": block.ToolUseID,
					"output":  block.Text,
				})
			}
		}
		parts := make([]any, 0, len(message.Content))
		for _, block := range message.Content {
			if part := responseInputPart(block); part != nil {
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			items = append(items, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": parts,
			})
		}
	}
	out["input"] = items

	var maxTokens *int
	if maxTokensDefault != nil && *maxTokensDefault != 0 {
		value := *maxTokensDefault
		maxTokens = &value
	}
	if request.MaxTokens != nil && *request.MaxTokens != 0 {
		maxTokens = request.MaxTokens
	}
	if maxTokens != nil {
		out["max_output_tokens"] = *maxTokens
	}
	if request.Temperature != nil {
		out["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		out["top_p"] = *request.TopP
	}
	if reasoning := reasoningPayload(request); len(reasoning) > 0 {
		out["reasoning"] = reasoning
	}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			parameters := tool.Parameters
			if len(parameters) == 0 {
				parameters = map[string]any{"type": "object"}
			}
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			})
		}
		out["tools"] = tools
	}
	if request.ToolChoice != nil && *request.ToolChoice != "" {
		choice := *request.ToolChoice
		if name, ok := strings.CutPrefix(choice, "tool:"); ok {
			out["tool_choice"] = map[string]any{"type": "function", "name": name}
		} else {
			out["tool_choice"] = choice
		}
	}
	if request.Stream {
		out["stream"] = true
	}

	for key, value := range request.Extra {
		if !responsesExtraKeys[key] || value == nil {
			continue
		}
		if _, exists := out[key]; exists {
			continue
		}
		out[key] = value
	}
	if _, exists := out["text"]; !exists {
		if format := chatResponseFormatToResponsesFormat(request.Extra["response_format"]); format != nil {
			out["text"] = map[string]any{"format": format}
		}
	}
	return out, nil
}
