package llmproxy

import (
	"strings"
)

// WireFormat identifies the request/response shape spoken on the wire. It is
// deliberately distinct from ProviderProtocol: a provider is authenticated as
// one vendor but may expose endpoints in several wire formats.
type WireFormat string

// ProviderProtocol identifies the vendor API a provider's credentials and
// default headers belong to.
type ProviderProtocol string

const (
	wireOpenAI    WireFormat = "openai"
	wireResponses WireFormat = "responses"
	wireAnthropic WireFormat = "anthropic"

	// ProtocolOpenAI and ProtocolAnthropic are the provider protocols
	// supported by the configuration.
	ProtocolOpenAI    ProviderProtocol = "openai"
	ProtocolAnthropic ProviderProtocol = "anthropic"
)

var supportedProtocols = []ProviderProtocol{ProtocolOpenAI, ProtocolAnthropic}

// protocolList renders the supported provider protocols for error messages.
func protocolList() string {
	names := make([]string, 0, len(supportedProtocols))
	for _, protocol := range supportedProtocols {
		names = append(names, string(protocol))
	}
	return strings.Join(names, ", ")
}

// apiPrefix is the path prefix under which the gateway exposes its HTTP API.
// Clients should use http://<host>:<port>/api as their base URL.
const apiPrefix = "/api"

// routeSpec is the single source of truth for a gateway route: which inbound
// path it serves, which provider endpoint implements it, and the wire format
// used on both sides. Routing, provider endpoint validation and the index
// document all derive from this table.
type routeSpec struct {
	wire     WireFormat
	protocol ProviderProtocol
	// configKey is the name a provider uses for this endpoint in the
	// configuration file.
	configKey string
	endpoint  string
	inbound   string
	// enabledByDefault marks endpoints a provider enables when its endpoints
	// field is omitted.
	enabledByDefault bool
	advertise        bool
}

var routeSpecs = []routeSpec{
	{
		wire: wireOpenAI, protocol: ProtocolOpenAI, configKey: "chat_completions",
		endpoint: "/chat/completions", inbound: apiPrefix + "/v1/chat/completions",
		enabledByDefault: true, advertise: true,
	},
	{
		wire: wireResponses, protocol: ProtocolOpenAI, configKey: "responses",
		endpoint: "/responses", inbound: apiPrefix + "/v1/responses",
		enabledByDefault: true, advertise: true,
	},
	{
		wire: wireAnthropic, protocol: ProtocolAnthropic, configKey: "messages",
		endpoint: "/v1/messages", inbound: apiPrefix + "/v1/messages",
		enabledByDefault: true, advertise: true,
	},
}

func routeSpecForInbound(path string) (routeSpec, bool) {
	for _, spec := range routeSpecs {
		if spec.inbound == path {
			return spec, true
		}
	}
	return routeSpec{}, false
}

// routeSpecForConfigKey resolves a provider endpoint name from the
// configuration file.
func routeSpecForConfigKey(key string) (routeSpec, bool) {
	for _, spec := range routeSpecs {
		if spec.configKey == key {
			return spec, true
		}
	}
	return routeSpec{}, false
}

// routeSpecsForProtocol returns the route table entries for one protocol.
func routeSpecsForProtocol(protocol ProviderProtocol) []routeSpec {
	specs := make([]routeSpec, 0, len(routeSpecs))
	for _, spec := range routeSpecs {
		if spec.protocol == protocol {
			specs = append(specs, spec)
		}
	}
	return specs
}

// defaultRouteSpecs returns the upstream endpoints a provider enables for a
// protocol when its endpoints field is omitted.
func defaultRouteSpecs(protocol ProviderProtocol) []routeSpec {
	specs := make([]routeSpec, 0, len(routeSpecs))
	for _, spec := range routeSpecsForProtocol(protocol) {
		if spec.enabledByDefault {
			specs = append(specs, spec)
		}
	}
	return specs
}

// wireForPath reports the wire format an inbound path speaks. Unknown paths
// fall back to the OpenAI envelope, matching the gateway's catch-all error
// shape.
func wireForPath(path string) WireFormat {
	if spec, ok := routeSpecForInbound(path); ok {
		return spec.wire
	}
	return wireOpenAI
}
