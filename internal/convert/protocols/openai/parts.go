package openai

import (
	"strings"

	"github.com/ratsdev/llm-proxy/internal/convert/ir"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// AssistantSegments is the protocol-neutral split of assistant content.
type AssistantSegments struct {
	Visible  []ir.Block
	Thinking []ir.Block
	Tools    []ir.Block
}

// SegmentAssistant groups assistant blocks so each wire formatter only serializes.
func SegmentAssistant(blocks []ir.Block) AssistantSegments {
	var segments AssistantSegments
	for _, block := range blocks {
		switch block.Type {
		case ir.Text, ir.Image, ir.Video:
			segments.Visible = append(segments.Visible, block)
		case ir.Thinking:
			segments.Thinking = append(segments.Thinking, block)
		case ir.ToolUse:
			segments.Tools = append(segments.Tools, block)
		}
	}
	return segments
}

func parseOpenAIPart(part any) (ir.Block, bool) {
	if text, ok := part.(string); ok {
		if text == "" {
			return ir.Block{}, false
		}
		return ir.TextBlock(text), true
	}
	object, ok := util.AsMap(part)
	if !ok {
		return ir.Block{}, false
	}

	switch util.StringValue(object["type"]) {
	case "text", "input_text", "output_text", "summary_text", "reasoning_text":
		text, ok := util.AsString(object["text"])
		if !ok {
			return ir.Block{}, false
		}
		return ir.TextBlock(text), true
	case "image_url", "input_image", "output_image":
		url := urlFrom(object["image_url"], object["url"])
		if url == "" {
			return ir.Block{}, false
		}
		return ir.Block{Type: ir.Image, ImageURL: url}, true
	case "video_url", "input_video", "video":
		url := urlFrom(object["video_url"], object["video"], object["url"])
		if url == "" {
			return ir.Block{}, false
		}
		return ir.Block{Type: ir.Video, VideoURL: url}, true
	}
	return ir.Block{}, false
}

func parseOpenAIContent(content any) []ir.Block {
	return protocols.BlocksFrom(content, parseOpenAIPart)
}

func thinkingText(blocks []ir.Block) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
