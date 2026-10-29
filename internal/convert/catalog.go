// Wire-format vocabulary and converter catalogue: the endpoint formats a client
// or upstream can speak, and the converters that map between them.
//
// Configuration (core) validates converter IDs against this catalogue and the
// adapters in this package implement them, so both read the same table instead
// of keeping parallel copies.

package convert

import (
	"fmt"
	"strings"
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
