package gateway

// Core types live in the core package so the gateway and the console can share
// them without an import cycle, and the routing engine lives in its own package
// so it can be tested without an HTTP stack. These aliases keep the gateway
// wiring readable.

import (
	"github.com/ratsdev/llm-proxy/internal/core"
	"github.com/ratsdev/llm-proxy/internal/router"
)

type (
	Config           = core.Config
	Provider         = core.Provider
	ProviderEndpoint = core.ProviderEndpoint
	ModelProvider    = core.ModelProvider
	Model            = core.Model
	User             = core.User
	Store            = core.Store
	WireFormat       = core.WireFormat
	routeSpec        = core.RouteSpec

	// RouteRequest and RouteResult are the routing engine's I/O types; the
	// gateway hands requests in and relays whatever comes back.
	RouteRequest = router.RouteRequest
	RouteResult  = router.RouteResult
)

const (
	DefaultDatabasePath = core.DefaultDatabasePath
	RoutingPriority     = core.RoutingPriority
	RoutingRoundRobin   = core.RoutingRoundRobin
	RoutingRandom       = core.RoutingRandom

	wireChatCompletions = core.WireChatCompletions
	wireResponses       = core.WireResponses
	wireMessages        = core.WireMessages
	apiPrefix           = core.APIPrefix
)

var routeSpecs = core.RouteSpecs()

// OpenStore opens (and creates when necessary) the SQLite database.
func OpenStore(path string) (*Store, error) {
	return core.OpenStore(path)
}

func wireForPath(path string) WireFormat {
	return core.WireForPath(path)
}
