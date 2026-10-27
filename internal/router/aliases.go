package router

import "github.com/ratsdev/llm-proxy/internal/core"

// The routing engine works with the shared configuration types from core.
// These aliases keep the strategy, retry and sticky code readable.
type (
	Config           = core.Config
	Provider         = core.Provider
	ProviderEndpoint = core.ProviderEndpoint
	ModelProvider    = core.ModelProvider
	Model            = core.Model
	WireFormat       = core.WireFormat
)

const (
	RoutingPriority   = core.RoutingPriority
	RoutingRoundRobin = core.RoutingRoundRobin
	RoutingRandom     = core.RoutingRandom

	wireChatCompletions = core.WireChatCompletions
	wireResponses       = core.WireResponses
	wireMessages        = core.WireMessages
)

// routeSpecs is the gateway route table; the router validates on start-up that
// every advertised wire format has a registered converter.
var routeSpecs = core.RouteSpecs()
