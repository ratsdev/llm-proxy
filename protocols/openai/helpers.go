package openai

import (
	"github.com/ratsdev/llm-proxy/ir"
	"github.com/ratsdev/llm-proxy/util"
)

// urlFrom returns the first value that carries a URL. Both bare strings and
// {"url": ...} objects are accepted because compatible providers disagree.
func urlFrom(values ...any) string {
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			if typed != "" {
				return typed
			}
		case map[string]any:
			if len(typed) > 0 {
				return util.StringValue(typed["url"])
			}
		}
	}
	return ""
}

// imageURL renders an image block as a URL, preferring an explicit URL and
// falling back to an inline data URL.
func imageURL(block ir.Block) string {
	if block.ImageURL != "" {
		return block.ImageURL
	}
	if block.Data == "" {
		return ""
	}
	return "data:" + block.MediaType + ";base64," + block.Data
}

// rewriteStringField replaces key when it already holds a string.
func rewriteStringField(object map[string]any, key, value string) bool {
	if _, ok := object[key].(string); !ok {
		return false
	}
	object[key] = value
	return true
}

// parseToolChoice normalises every tool_choice shape used by the two OpenAI
// wire formats: the "auto"/"none"/"required" keywords, a named function, or
// the Chat {"type":"function","function":{"name":...}} object.
func parseToolChoice(raw any) *string {
	if name, ok := raw.(string); ok {
		switch name {
		case "auto", "none", "required":
			return &name
		}
		return nil
	}
	object, ok := util.AsMap(raw)
	if !ok {
		return nil
	}
	if kind := util.StringValue(object["type"]); kind != "" && kind != "function" {
		return nil
	}
	name := util.StringValue(object["name"])
	if name == "" {
		if function, ok := util.AsMap(object["function"]); ok {
			name = util.StringValue(function["name"])
		}
	}
	if name == "" {
		return nil
	}
	choice := "tool:" + name
	return &choice
}
