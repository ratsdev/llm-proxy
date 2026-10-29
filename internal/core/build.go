package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/util"
)

func buildProvider(input ProviderInput) (*Provider, error) {
	id, err := configID("provider", input.ID)
	if err != nil {
		return nil, err
	}
	where := fmt.Sprintf("providers.%s", id)
	maxRetries := 2
	if input.MaxRetries != nil {
		if *input.MaxRetries < 0 || *input.MaxRetries > maxProviderRetries {
			return nil, fmt.Errorf("%s.max_retries must be an integer between 0 and %d", where, maxProviderRetries)
		}
		maxRetries = *input.MaxRetries
	}
	timeout, err := validateTimeout(input.Timeout, where+".timeout")
	if err != nil {
		return nil, err
	}
	baseURL, err := normalizeBaseURL(input.BaseURL, where+".base_url")
	if err != nil {
		return nil, err
	}
	endpoints, err := normalizeProviderEndpoints(input.Endpoints, where)
	if err != nil {
		return nil, err
	}
	proxy, err := validateProxy(input.Proxy, where+".proxy")
	if err != nil {
		return nil, err
	}
	headers := input.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	if err := validateHeaders(headers, where+".headers"); err != nil {
		return nil, err
	}
	return &Provider{
		ID:             id,
		Name:           strings.TrimSpace(input.Name),
		BaseURL:        baseURL,
		APIKey:         strings.TrimSpace(input.APIKey),
		MaxRetries:     maxRetries,
		Timeout:        timeout,
		Endpoints:      endpoints,
		Headers:        headers,
		Proxy:          proxy,
		UpstreamModels: normalizeStringList(input.UpstreamModels),
	}, nil
}

func buildModel(input ModelInput) (*Model, error) {
	id, err := configID("model", input.ID)
	if err != nil {
		return nil, err
	}
	where := fmt.Sprintf("models.%s", id)
	if len(input.Providers) == 0 {
		return nil, fmt.Errorf("%s.providers must be a non-empty array", where)
	}
	providers := make([]ModelProvider, 0, len(input.Providers))
	type bindingKey struct{ providerID, upstreamModel string }
	seenBindings := make(map[bindingKey]int, len(input.Providers))
	for index, raw := range input.Providers {
		providerWhere := fmt.Sprintf("%s.providers[%d]", where, index)
		providerID := strings.TrimSpace(raw.ProviderID)
		if providerID == "" {
			return nil, fmt.Errorf("%s.provider_id must be a non-empty string", providerWhere)
		}
		upstreamModel := strings.TrimSpace(raw.UpstreamModel)
		if upstreamModel == "" {
			return nil, fmt.Errorf("%s.upstream_model must be a non-empty string", providerWhere)
		}
		maxRetryAfter, err := validateMaxRetryAfter(raw.MaxRetryAfter, providerWhere+".max_retry_after")
		if err != nil {
			return nil, err
		}
		binding := ModelProvider{
			ProviderID:    providerID,
			UpstreamModel: upstreamModel,
			MaxRetryAfter: maxRetryAfter,
		}
		// The store keys bindings by (model, provider, upstream model), so a
		// duplicate would only surface as a SQLite constraint error after the
		// form was accepted. Reject it here with a message that points at the row.
		key := bindingKey{providerID: providerID, upstreamModel: upstreamModel}
		if first, ok := seenBindings[key]; ok {
			return nil, fmt.Errorf("%s duplicates providers[%d]: provider %q already serves %q",
				providerWhere, first, providerID, upstreamModel)
		}
		seenBindings[key] = index
		providers = append(providers, binding)
	}
	routing := strings.TrimSpace(input.Routing)
	if routing == "" {
		routing = RoutingPriority
	}
	switch routing {
	case RoutingPriority, RoutingRoundRobin, RoutingRandom:
	default:
		return nil, fmt.Errorf("%s.routing must be %q, %q or %q", where,
			RoutingPriority, RoutingRoundRobin, RoutingRandom)
	}
	if err := validatePositiveInt(input.ContextLength, "context_length", where); err != nil {
		return nil, err
	}
	if err := validatePositiveInt(input.MaxOutputLength, "max_output_length", where); err != nil {
		return nil, err
	}
	timeout, err := validateTimeout(input.Timeout, where+".timeout")
	if err != nil {
		return nil, err
	}
	modalities := []string{"text"}
	outputModalities := []string{"text"}
	if len(input.Modalities) > 0 {
		modalities = append([]string(nil), input.Modalities...)
	}
	if len(input.OutputModalities) > 0 {
		outputModalities = append([]string(nil), input.OutputModalities...)
	}
	return &Model{
		ID:               id,
		Name:             strings.TrimSpace(input.Name),
		Providers:        providers,
		Routing:          routing,
		ContextLength:    input.ContextLength,
		MaxOutputLength:  input.MaxOutputLength,
		Modalities:       modalities,
		OutputModalities: outputModalities,
		Reasoning:        input.Reasoning,
		Tools:            input.Tools,
		Timeout:          timeout,
	}, nil
}

// modelInput renders a normalised model as editable input. A save returns the
// value the next configuration snapshot will contain, so defaults, trimmed
// identifiers and resolved bindings are visible to the caller immediately.
func modelInput(model *Model) ModelInput {
	bindings := make([]ModelProviderInput, 0, len(model.Providers))
	for _, binding := range model.Providers {
		bindings = append(bindings, ModelProviderInput(binding))
	}
	return ModelInput{
		ID:               model.ID,
		Name:             model.Name,
		Providers:        bindings,
		Routing:          model.Routing,
		ContextLength:    model.ContextLength,
		MaxOutputLength:  model.MaxOutputLength,
		Modalities:       model.Modalities,
		OutputModalities: model.OutputModalities,
		Reasoning:        model.Reasoning,
		Tools:            model.Tools,
		Timeout:          model.Timeout,
	}
}

func endpointConfig(endpoints map[WireFormat]ProviderEndpoint) map[string]ProviderEndpointInput {
	out := make(map[string]ProviderEndpointInput, len(endpoints))
	for format, endpoint := range endpoints {
		out[string(format)] = ProviderEndpointInput{
			Path:      endpoint.Path,
			Converter: endpoint.ConverterID(),
		}
	}
	return out
}

func decodeJSON(raw string, target any) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), target)
}

func encodeJSON(value any) (string, error) {
	encoded, err := util.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// normalizeStringList trims, drops empty entries, and removes duplicates while
// preserving the operator's ordering.
func normalizeStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
