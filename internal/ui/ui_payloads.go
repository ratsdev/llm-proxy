package ui

import (
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/ratsdev/llm-proxy/internal/util"
)

func (h *Handler) handleUIListModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdmin(w, r); !ok {
		return
	}
	cfg := h.config()
	models := uiModels(cfg)
	payloads := make([]map[string]any, 0, len(models))
	for _, model := range models {
		payloads = append(payloads, uiModelPayload(model, cfg))
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"models": payloads})
}

// handleUICatalog lists the models the signed-in account may call. Unlike the
// administrator model view it never exposes provider bindings, so members can
// see what their allow-list unlocks without learning upstream routing details.
func (h *Handler) handleUICatalog(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.requireUIUser(w, r)
	if !ok {
		return
	}
	cfg := h.config()
	models := make([]map[string]any, 0, len(cfg.Models))
	for _, model := range uiModels(cfg) {
		if !user.CanUse(model.ID) {
			continue
		}
		models = append(models, uiCatalogModelPayload(model))
	}
	writeUIJSON(w, http.StatusOK, map[string]any{
		"models": models,
		"total":  len(cfg.Models),
	})
}

func (h *Handler) handleUIListProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdmin(w, r); !ok {
		return
	}
	cfg := h.config()
	modelsByProvider := providerModels(cfg)
	providerIDs := make([]string, 0, len(cfg.Providers))
	for id := range cfg.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)

	payloads := make([]map[string]any, 0, len(providerIDs))
	for _, id := range providerIDs {
		provider := cfg.Providers[id]
		payloads = append(payloads, uiProviderPayload(provider, modelsByProvider[id], h.defaultTimeout))
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"providers": payloads})
}

func uiModels(cfg *Config) []*Model {
	models := make([]*Model, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func uiModelPayload(model *Model, cfg *Config) map[string]any {
	bindings := make([]map[string]any, 0, len(model.Providers))
	for _, binding := range model.Providers {
		provider, ok := cfg.Providers[binding.ProviderID]
		endpoints := []string(nil)
		name := ""
		if ok {
			endpoints = provider.EndpointNames()
			name = provider.Name
		}
		bindings = append(bindings, map[string]any{
			"id":             binding.ProviderID,
			"name":           name,
			"endpoints":      endpoints,
			"upstream_model": binding.UpstreamModel,
		})
	}
	return map[string]any{
		"id":                model.ID,
		"name":              model.Name,
		"routing":           model.Routing,
		"modalities":        model.Modalities,
		"output_modalities": model.OutputModalities,
		"reasoning":         model.Reasoning,
		"tools":             model.Tools,
		"providers":         bindings,
		"context_length":    util.Nullable(model.ContextLength),
		"max_output_length": util.Nullable(model.MaxOutputLength),
		"timeout":           util.Nullable(model.Timeout),
	}
}

// uiCatalogModelPayload is the member-facing model shape. It deliberately
// omits binding order and upstream identifiers.
func uiCatalogModelPayload(model *Model) map[string]any {
	return map[string]any{
		"id":                model.ID,
		"name":              model.Name,
		"modalities":        model.Modalities,
		"output_modalities": model.OutputModalities,
		"reasoning":         model.Reasoning,
		"tools":             model.Tools,
		"context_length":    util.Nullable(model.ContextLength),
		"max_output_length": util.Nullable(model.MaxOutputLength),
		"providers":         len(model.Providers),
	}
}

func uiProviderPayload(provider *Provider, models []*Model, defaultTimeout time.Duration) map[string]any {
	proxyScheme := ""
	if provider.Proxy != "" {
		if parsed, err := url.Parse(provider.Proxy); err == nil {
			proxyScheme = parsed.Scheme
		}
	}
	timeout := any(defaultTimeout.Seconds())
	if provider.Timeout != nil {
		timeout = *provider.Timeout
	}
	modelIDs := make([]string, 0, len(models))
	modelNames := make(map[string]string, len(models))
	for _, model := range models {
		modelIDs = append(modelIDs, model.ID)
		if model.Name != "" {
			modelNames[model.ID] = model.Name
		}
	}
	sort.Strings(modelIDs)
	endpoints := make(map[string]map[string]string, len(provider.Endpoints))
	for wire, endpoint := range provider.Endpoints {
		endpoints[string(wire)] = map[string]string{
			"path":      endpoint.Path,
			"converter": endpoint.ConverterID(),
		}
	}
	return map[string]any{
		"id":              provider.ID,
		"name":            provider.Name,
		"base_url":        displayBaseURL(provider.BaseURL),
		"endpoints":       endpoints,
		"max_retries":     provider.MaxRetries,
		"timeout":         timeout,
		"has_key":         provider.APIKey != "",
		"proxy":           provider.Proxy != "",
		"proxy_scheme":    proxyScheme,
		"custom_headers":  len(provider.Headers) > 0,
		"upstream_models": provider.UpstreamModels,
		"models":          modelIDs,
		"model_names":     modelNames,
	}
}

// displayBaseURL hides credentials and query strings that operators may have
// embedded in a provider URL. It is display-only; upstream calls keep the
// configured URL unchanged.
func displayBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// providerModels groups every model by the providers it is bound to. A model
// may bind the same provider twice with different upstream model names, but it
// is still one entry in the provider's list.
func providerModels(cfg *Config) map[string][]*Model {
	modelsByProvider := make(map[string][]*Model)
	seen := make(map[string]map[string]bool)
	for _, model := range cfg.Models {
		for _, binding := range model.Providers {
			if _, ok := cfg.Providers[binding.ProviderID]; !ok {
				continue
			}
			if seen[binding.ProviderID] == nil {
				seen[binding.ProviderID] = make(map[string]bool)
			}
			if seen[binding.ProviderID][model.ID] {
				continue
			}
			seen[binding.ProviderID][model.ID] = true
			modelsByProvider[binding.ProviderID] = append(modelsByProvider[binding.ProviderID], model)
		}
	}
	return modelsByProvider
}
