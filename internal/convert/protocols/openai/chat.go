// Package openai implements the OpenAI Chat Completions and Responses wire
// formats.
package openai

import (
	"fmt"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/util"
)

const (
	textBlockIndex     = 0
	thinkingBlockIndex = 1
)

// chatExtraKeys are the only Chat fields that must survive a cross-protocol
// hop. Same-protocol traffic forwards the original JSON and does not use Extra.
var chatExtraKeys = map[string]bool{
	"reasoning":             true,
	"reasoning_effort":      true,
	"thinking":              true,
	"text":                  true,
	"response_format":       true,
	"max_completion_tokens": true,
	"metadata":              true,
	"store":                 true,
	"parallel_tool_calls":   true,
	"service_tier":          true,
}

// nonChatKeys are Extra fields that must not be copied onto a Chat body.
// They are either mapped onto Chat-native keys or are Responses-only.
var nonChatKeys = map[string]bool{
	"thinking":             true,
	"reasoning":            true,
	"text":                 true,
	"previous_response_id": true,
	"include":              true,
	"truncation":           true,
}

// Chat adapts the OpenAI Chat Completions API.
type Chat struct{}

func parseStop(raw any) []string {
	if text, ok := raw.(string); ok {
		return []string{text}
	}
	items, ok := util.AsSlice(raw)
	if !ok {
		return nil
	}
	stop := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			stop = append(stop, text)
		}
	}
	return stop
}

func contentPayload(blocks []ir.Block) any {
	if len(blocks) == 0 {
		return nil
	}
	allText := true
	for _, block := range blocks {
		if block.Type != ir.Text {
			allText = false
			break
		}
	}
	if allText {
		var b strings.Builder
		for _, block := range blocks {
			b.WriteString(block.Text)
		}
		return b.String()
	}

	parts := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ir.Text:
			parts = append(parts, map[string]any{"type": "text", "text": block.Text})
		case ir.Image:
			if url := imageURL(block); url != "" {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
			}
		case ir.Video:
			if block.VideoURL != "" {
				parts = append(parts, map[string]any{
					"type":      "video_url",
					"video_url": map[string]any{"url": block.VideoURL},
				})
			}
		}
	}
	return parts
}

func toolCallPayload(block ir.Block, position int) map[string]any {
	id := block.ToolUseID
	if id == "" {
		id = fmt.Sprintf("call_%d", position)
	}
	arguments := block.ToolInput
	if arguments == "" {
		arguments = "{}"
	}
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      block.ToolName,
			"arguments": arguments,
		},
	}
}
