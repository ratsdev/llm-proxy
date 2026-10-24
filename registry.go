package llmproxy

import (
	"github.com/ratsdev/llm-proxy/protocols"
	"github.com/ratsdev/llm-proxy/protocols/anthropic"
	"github.com/ratsdev/llm-proxy/protocols/openai"
)

// adapters is immutable after start-up; every adapter is stateless. The table
// must cover every wire format in routeSpecs: NewRouter validates that on
// start-up and registry_test enforces it for the table itself, so request
// paths can look up an adapter without an error branch.
var adapters = map[WireFormat]protocols.Adapter{
	wireOpenAI:    openai.Chat{},
	wireResponses: openai.Responses{},
	wireAnthropic: anthropic.Messages{},
}

func adapterFor(wire WireFormat) protocols.Adapter {
	return adapters[wire]
}
