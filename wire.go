package llmproxy

import (
	"slices"
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
// path it serves, which provider endpoint implements it, the wire format used
// on both sides, and the operations the endpoint can serve. Routing, provider
// endpoint validation and the index document all derive from this table.
type routeSpec struct {
	wire      WireFormat
	protocol  ProviderProtocol
	endpoint  string
	inbound   string
	operation string
	serves    []string
	isDefault bool
	advertise bool
}

var routeSpecs = []routeSpec{
	{
		wire: wireOpenAI, protocol: ProtocolOpenAI,
		endpoint: "/chat/completions", inbound: apiPrefix + "/v1/chat/completions",
		operation: "chat", serves: []string{"chat", "responses"},
		isDefault: true, advertise: true,
	},
	{
		wire: wireResponses, protocol: ProtocolOpenAI,
		endpoint: "/responses", inbound: apiPrefix + "/v1/responses",
		operation: "responses", serves: []string{"chat", "responses"},
		advertise: true,
	},
	{
		wire: wireOpenAI, protocol: ProtocolOpenAI,
		endpoint: "/embeddings", inbound: apiPrefix + "/v1/embeddings",
		operation: "embeddings", serves: []string{"embeddings"},
	},
	{
		wire: wireAnthropic, protocol: ProtocolAnthropic,
		endpoint: "/messages", inbound: apiPrefix + "/v1/messages",
		operation: "chat", serves: []string{"chat", "responses"},
		isDefault: true, advertise: true,
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

func routeSpecForEndpoint(path string) (routeSpec, bool) {
	for _, spec := range routeSpecs {
		if spec.endpoint == path {
			return spec, true
		}
	}
	return routeSpec{}, false
}

func endpointAllowed(protocol ProviderProtocol, path string) bool {
	spec, ok := routeSpecForEndpoint(path)
	return ok && spec.protocol == protocol
}

// routeSpecForOperation resolves the route spec for an endpoint when that
// endpoint implements the given operation.
func routeSpecForOperation(path, operation string) (routeSpec, bool) {
	spec, ok := routeSpecForEndpoint(path)
	if !ok || !slices.Contains(spec.serves, operation) {
		return routeSpec{}, false
	}
	return spec, true
}

func defaultEndpoint(protocol ProviderProtocol) string {
	for _, spec := range routeSpecs {
		if spec.protocol == protocol && spec.isDefault {
			return spec.endpoint
		}
	}
	return ""
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

// chatFamily reports whether the route speaks a chat-family protocol that can
// be translated across wires. Embeddings stay an opaque payload.
func (s routeSpec) chatFamily() bool {
	return s.operation != "embeddings"
}
