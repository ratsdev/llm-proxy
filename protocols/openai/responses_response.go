package openai

import (
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/protocols"
	"github.com/ratsdev/llm-proxy/util"
)

// ParseResponse converts a Responses payload into the IR.
func (Responses) ParseResponse(payload map[string]any) (*ir.Response, error) {
	content := parseResponseOutput(payload["output"])
	stopReason := responseStopReason(payload, content)
	created := time.Now().Unix()
	if value, ok := util.AsInt(payload["created_at"]); ok {
		created = int64(value)
	}
	usage, _ := util.AsMap(payload["usage"])

	return &ir.Response{
		ID:         util.StringValue(payload["id"]),
		Model:      util.StringValue(payload["model"]),
		Content:    content,
		StopReason: &stopReason,
		Usage:      protocols.ParseUsage(usage, "input_tokens", "output_tokens"),
		Created:    created,
	}, nil
}

// FormatResponse converts the IR into a Responses payload.
func (Responses) FormatResponse(response *ir.Response, model string) map[string]any {
	output := make([]any, 0)
	textParts := make([]string, 0)
	flushText := func() {
		if len(textParts) > 0 {
			output = append(output, responseMessageItem(strings.Join(textParts, ""), ""))
			textParts = textParts[:0]
		}
	}

	for _, block := range response.Content {
		switch block.Type {
		case ir.Thinking:
			if block.Text == "" && !ir.IsEncryptedReasoning(block.Signature) {
				continue
			}
			flushText()
			output = append(output, reasoningItem(block.Text, block.Signature, ""))
		case ir.Text:
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case ir.ToolUse:
			flushText()
			output = append(output, responseFunctionCallItem(block.ToolUseID, block.ToolName, block.ToolInput, ""))
		}
	}
	flushText()

	id := response.ID
	if id == "" {
		id = "resp_" + util.RandomHex(24)
	}
	created := response.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	return responsePayload(id, model, created, output, response.Usage, response.StopReason)
}

// FormatError renders the OpenAI error envelope used by the Responses API.
func (Responses) FormatError(status int, message, errorType, code, requestID string) map[string]any {
	return Chat{}.FormatError(status, message, errorType, code, requestID)
}

// RewriteModel replaces the public model name in a Responses payload, which
// carries it both at the top level and inside the response envelope used by
// streaming events.
func (Responses) RewriteModel(payload map[string]any, model string) bool {
	changed := rewriteStringField(payload, "model", model)
	if response, ok := util.AsMap(payload["response"]); ok {
		changed = rewriteStringField(response, "model", model) || changed
	}
	return changed
}
