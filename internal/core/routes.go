package core

import (
	"strings"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

// APIPrefix is the path prefix under which the gateway exposes its HTTP API.
// Clients should use http://<host>:<port>/api as their base URL.
const APIPrefix = "/api"

// RouteSpec is the single source of truth for a gateway route: which inbound
// path it serves, which endpoint format it speaks, and the default upstream
// path used when a provider does not override it. Routing, provider endpoint
// validation and the index document all derive from this table.
type RouteSpec struct {
	Wire      WireFormat
	Endpoint  string
	Inbound   string
	Advertise bool
}

// RouteSpecs returns the gateway routes in canonical order. The slice is a copy
// so a caller cannot reorder the routing table for the rest of the process.
func RouteSpecs() []RouteSpec {
	return append([]RouteSpec(nil), routeSpecs...)
}

var routeSpecs = []RouteSpec{
	{
		Wire: WireChatCompletions, Endpoint: "/chat/completions",
		Inbound: APIPrefix + "/v1/chat/completions", Advertise: true,
	},
	{
		Wire: WireResponses, Endpoint: "/responses",
		Inbound: APIPrefix + "/v1/responses", Advertise: true,
	},
	{
		Wire: WireMessages, Endpoint: "/v1/messages",
		Inbound: APIPrefix + "/v1/messages", Advertise: true,
	},
}

// RouteSpecForInbound finds the route serving an inbound path.
func RouteSpecForInbound(path string) (RouteSpec, bool) {
	for _, spec := range routeSpecs {
		if spec.Inbound == path {
			return spec, true
		}
	}
	return RouteSpec{}, false
}

// RouteSpecForWire resolves an endpoint format from the stored provider
// endpoint table.
func RouteSpecForWire(wire WireFormat) (RouteSpec, bool) {
	for _, spec := range routeSpecs {
		if spec.Wire == wire {
			return spec, true
		}
	}
	return RouteSpec{}, false
}

// WireForPath reports the wire format an inbound path speaks. Unknown paths
// fall back to the Chat Completions envelope, matching the gateway's catch-all
// error shape.
func WireForPath(path string) WireFormat {
	if spec, ok := RouteSpecForInbound(path); ok {
		return spec.Wire
	}
	return WireChatCompletions
}

// Wire formats: the endpoint shapes a provider may serve.

// WireFormat identifies one gateway endpoint and the request/response shape
// used on both sides of the proxy. The three formats are independent: a
// provider configures each endpoint it supports separately.
//
// It aliases convert.Format so routing, configuration and the adapter registry
// share one type instead of converting between two parallel enums.
type WireFormat = convert.Format

// Gateway wire formats. A provider may serve any subset of them, and each one
// has its own inbound route and default upstream path.
const (
	WireChatCompletions WireFormat = convert.ChatCompletions
	WireResponses       WireFormat = convert.Responses
	WireMessages        WireFormat = convert.Messages
)

// WireFormats lists the supported formats in canonical order.
var WireFormats = []WireFormat{WireChatCompletions, WireResponses, WireMessages}

// WireFormatList renders the supported endpoint formats for error messages.
func WireFormatList() string {
	names := make([]string, 0, len(WireFormats))
	for _, format := range WireFormats {
		names = append(names, string(format))
	}
	return strings.Join(names, ", ")
}
