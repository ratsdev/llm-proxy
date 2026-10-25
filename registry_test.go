package llmproxy

import (
	"testing"
)

// The adapter registry and the route table are two halves of the same
// knowledge; a missing entry must fail here rather than panic in a request.
func TestRegistryCoversEveryRoute(t *testing.T) {
	t.Parallel()

	for _, spec := range routeSpecs {
		if adapterFor(spec.wire) == nil {
			t.Errorf("wire format %q has no adapter", spec.wire)
		}
	}
	for wire := range adapters {
		referenced := false
		for _, spec := range routeSpecs {
			if spec.wire == wire {
				referenced = true
				break
			}
		}
		if !referenced {
			t.Errorf("adapter %q is not referenced by any route", wire)
		}
	}
}

func TestRouteSpecsAreConsistent(t *testing.T) {
	t.Parallel()

	inbounds := make(map[string]bool)
	endpoints := make(map[string]bool)
	configKeys := make(map[string]bool)
	for _, spec := range routeSpecs {
		if inbounds[spec.inbound] {
			t.Errorf("duplicate inbound path %q", spec.inbound)
		}
		if endpoints[spec.endpoint] {
			t.Errorf("duplicate provider endpoint %q", spec.endpoint)
		}
		if configKeys[spec.configKey] {
			t.Errorf("duplicate endpoint config key %q", spec.configKey)
		}
		inbounds[spec.inbound] = true
		endpoints[spec.endpoint] = true
		configKeys[spec.configKey] = true
		if _, err := endpointPath(spec.endpoint, spec.configKey); err != nil {
			t.Error(err)
		}
	}
	for _, protocol := range supportedProtocols {
		if len(defaultRouteSpecs(protocol)) == 0 {
			t.Errorf("protocol %q has no default endpoint", protocol)
		}
	}
}
