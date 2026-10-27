// Package convert exposes the pairwise wire-format conversions the gateway can
// perform. The actual field mapping lives in the protocol adapters under
// convert/protocols/; this package keeps the converter catalogue, the
// request/response entry points and the protocol-native error envelopes
// together.
package convert

import (
	"fmt"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/convert/protocols"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols/anthropic"
	"github.com/ratsdev/llm-proxy/internal/convert/protocols/openai"
)

// Format is a wire format spoken by clients and upstreams.
type Format string

// Wire formats. Each one names both an inbound endpoint type and the shape of
// the request and response bodies that travel over it.
const (
	ChatCompletions Format = "chat_completions"
	Responses       Format = "responses"
	Messages        Format = "messages"
)

var formats = []Format{ChatCompletions, Responses, Messages}

// None disables conversion: the request keeps the inbound wire format.
const None = "none"

// Converter IDs. They follow the "source_to_target" naming used by New API so
// an operator can tell the direction from the configuration alone.
const (
	OpenAIChatToClaudeMessages      = "openai_chat_to_claude_messages"
	ClaudeMessagesToOpenAIChat      = "claude_messages_to_openai_chat"
	OpenAIChatToOpenAIResponses     = "openai_chat_to_openai_responses"
	OpenAIResponsesToOpenAIChat     = "openai_responses_to_openai_chat"
	ClaudeMessagesToOpenAIResponses = "claude_messages_to_openai_responses"
	OpenAIResponsesToClaudeMessages = "openai_responses_to_claude_messages"
)

// Definition describes one selectable converter.
type Definition struct {
	ID      string `json:"id"`
	From    Format `json:"from"`
	To      Format `json:"to"`
	Label   string `json:"label"`
	Quality string `json:"quality"`
}

var definitions = []Definition{
	{
		ID: OpenAIChatToClaudeMessages, From: ChatCompletions, To: Messages,
		Label: "OpenAI Chat → Claude Messages", Quality: "fair",
	},
	{
		ID: ClaudeMessagesToOpenAIChat, From: Messages, To: ChatCompletions,
		Label: "Claude Messages → OpenAI Chat", Quality: "fair",
	},
	{
		ID: OpenAIChatToOpenAIResponses, From: ChatCompletions, To: Responses,
		Label: "OpenAI Chat → OpenAI Responses", Quality: "good",
	},
	{
		ID: OpenAIResponsesToOpenAIChat, From: Responses, To: ChatCompletions,
		Label: "OpenAI Responses → OpenAI Chat", Quality: "good",
	},
	{
		ID: ClaudeMessagesToOpenAIResponses, From: Messages, To: Responses,
		Label: "Claude Messages → OpenAI Responses", Quality: "fair",
	},
	{
		ID: OpenAIResponsesToClaudeMessages, From: Responses, To: Messages,
		Label: "OpenAI Responses → Claude Messages", Quality: "fair",
	},
}

// Formats returns the supported wire formats in canonical order.
func Formats() []Format {
	return append([]Format(nil), formats...)
}

// Definitions returns the selectable converters.
func Definitions() []Definition {
	return append([]Definition(nil), definitions...)
}

// Lookup finds a converter by ID. The "none" converter is included so callers
// can validate every endpoint with one code path.
func Lookup(id string) (Definition, bool) {
	id = strings.TrimSpace(id)
	if id == "" || id == None {
		return Definition{ID: None, Label: "None", Quality: "good"}, true
	}
	for _, definition := range definitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return Definition{}, false
}

// DefinitionFor resolves a converter for an endpoint whose inbound format is
// from. It rejects a converter that converts from a different protocol.
func DefinitionFor(from Format, id string) (Definition, error) {
	definition, ok := Lookup(id)
	if !ok {
		return Definition{}, fmt.Errorf("converter %q is not supported (expected one of: %s)",
			id, inboundConverterList(from))
	}
	if definition.ID == None {
		definition.From = from
		definition.To = from
		return definition, nil
	}
	if definition.From != from {
		return Definition{}, fmt.Errorf("converter %q converts from %s, not %s",
			definition.ID, definition.From, from)
	}
	return definition, nil
}

// inboundConverterList renders the converters that can serve an inbound format.
func inboundConverterList(from Format) string {
	names := []string{None}
	for _, definition := range definitions {
		if definition.From == from {
			names = append(names, definition.ID)
		}
	}
	return strings.Join(names, ", ")
}

// AdapterFor returns the adapter for a wire format, or nil when the format is
// unknown. The registry lives here so the gateway and the conversion entry
// points cannot disagree about which adapters exist.
func AdapterFor(format Format) protocols.Adapter {
	switch format {
	case ChatCompletions:
		return openai.Chat{}
	case Responses:
		return openai.Responses{}
	case Messages:
		return anthropic.Messages{}
	default:
		return nil
	}
}

// Request converts a request body from one wire format to another. The original
// map is never mutated.
func Request(from, to Format, body map[string]any, upstreamModel string) (map[string]any, error) {
	if body == nil {
		return nil, fmt.Errorf("missing request body")
	}
	if from == to {
		out := cloneObject(body)
		out["model"] = upstreamModel
		return out, nil
	}
	source, target := AdapterFor(from), AdapterFor(to)
	if source == nil || target == nil {
		return nil, fmt.Errorf("no converter from %s to %s", from, to)
	}
	request, err := source.ParseRequest(body)
	if err != nil {
		return nil, fmt.Errorf("convert request from %s: %w", from, err)
	}
	converted, err := target.FormatRequest(request, upstreamModel, nil)
	if err != nil {
		return nil, fmt.Errorf("convert request to %s: %w", to, err)
	}
	return converted, nil
}

// Response converts a response body from one wire format to another. The
// original map is never mutated.
func Response(from, to Format, body map[string]any, publicModel string) (map[string]any, error) {
	if body == nil {
		return nil, fmt.Errorf("missing response body")
	}
	if from == to {
		out := cloneObject(body)
		if target := AdapterFor(to); target != nil {
			target.RewriteModel(out, publicModel)
		}
		return out, nil
	}
	source, target := AdapterFor(from), AdapterFor(to)
	if source == nil || target == nil {
		return nil, fmt.Errorf("no converter from %s to %s", from, to)
	}
	response, err := source.ParseResponse(body)
	if err != nil {
		return nil, fmt.Errorf("convert response from %s: %w", from, err)
	}
	return target.FormatResponse(response, publicModel), nil
}

func cloneObject(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for key, value := range body {
		out[key] = cloneValue(value)
	}
	return out
}

// cloneValue copies nested JSON containers so rewriting a nested model name
// cannot mutate the caller's payload.
func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneValue(item)
		}
		return out
	default:
		return value
	}
}

// FormatError renders the error envelope for a wire format.
func FormatError(format Format, status int, message, errorType, code, requestID string) map[string]any {
	target := AdapterFor(format)
	if target == nil {
		return nil
	}
	return target.FormatError(status, message, errorType, code, requestID)
}

// StreamErrorFrames renders a terminal SSE error event for a wire format.
func StreamErrorFrames(format Format, message string) [][]byte {
	target := AdapterFor(format)
	if target == nil {
		return nil
	}
	return target.NewEncoder("", false).EncodeStreamError(message)
}
