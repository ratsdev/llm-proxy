package core

import (
	"net/url"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

// Provider is one upstream service. All of its endpoints share one base URL
// and each gateway endpoint adds its own upstream path.
type Provider struct {
	ID             string
	Name           string
	BaseURL        string
	APIKey         string
	MaxRetries     int
	Timeout        *float64
	Endpoints      map[WireFormat]ProviderEndpoint
	Headers        map[string]string
	Proxy          string
	UpstreamModels []string
}

// ProviderEndpoint is one configured upstream endpoint. Path is appended to the
// provider's shared BaseURL. Converter names the transformation applied to the
// inbound request and upstream response; "none" forwards the inbound wire
// format unchanged.
type ProviderEndpoint struct {
	Path      string
	Converter string
}

// ConverterID returns the configured converter, or "none" when conversion is
// disabled.
func (e ProviderEndpoint) ConverterID() string {
	if e.Converter == "" {
		return convert.None
	}
	return e.Converter
}

// UpstreamFormat resolves the wire format sent to the upstream. The bool is
// false when the converter is unknown or converts from a different inbound format.
func (e ProviderEndpoint) UpstreamFormat(inbound WireFormat) (WireFormat, bool) {
	definition, err := convert.DefinitionFor(inbound, e.ConverterID())
	if err != nil {
		return "", false
	}
	return definition.To, true
}

// EndpointNames lists the configured endpoint formats in canonical order.
func (p *Provider) EndpointNames() []string {
	names := make([]string, 0, len(p.Endpoints))
	for _, format := range WireFormats {
		if _, ok := p.Endpoints[format]; ok {
			names = append(names, string(format))
		}
	}
	return names
}

// URLFor joins the provider's base URL with an endpoint path. The base URL may
// carry a query string (some providers, such as Azure OpenAI, require an
// api-version parameter), so the path is appended to the URL path instead of
// concatenating the two strings.
func (p *Provider) URLFor(endpoint ProviderEndpoint) string {
	joined, err := url.JoinPath(p.BaseURL, endpoint.Path)
	if err != nil {
		return p.BaseURL + endpoint.Path
	}
	return joined
}

// Routing strategies select the first upstream for a conversation. Once an
// upstream has served a request, the session stays pinned to it regardless of
// the strategy.
const (
	// RoutingPriority tries providers in the order they are declared.
	RoutingPriority = "priority"
	// RoutingRoundRobin rotates the starting provider for each new session.
	RoutingRoundRobin = "round_robin"
	// RoutingRandom shuffles the candidate order for each new session.
	RoutingRandom = "random"
)

// ModelProvider binds a model to one provider's upstream model name.
type ModelProvider struct {
	ProviderID    string
	UpstreamModel string
	// MaxRetryAfter caps the Retry-After wait for this binding in seconds.
	// Nil uses the gateway default.
	MaxRetryAfter *float64
}

// Model is a caller-visible model with its upstream providers.
type Model struct {
	ID        string
	Name      string
	Providers []ModelProvider
	// Routing selects how a new session picks its first provider. It is one
	// of RoutingPriority, RoutingRoundRobin or RoutingRandom.
	Routing          string
	ContextLength    *int
	MaxOutputLength  *int
	Modalities       []string
	OutputModalities []string
	// Reasoning and Tools advertise optional request features to clients.
	Reasoning bool
	Tools     bool
	Timeout   *float64
}

// User is a console account. Every user signs in with a password and creates
// API keys that authenticate gateway requests; administrators additionally
// manage gateway configuration.
type User struct {
	ID            string
	Name          string
	AllowedModels []string
	IsAdmin       bool
	PasswordHash  string
}

// CanUse reports whether the user may call a model. Administrators bypass the
// allow-list. An empty list grants no models; "*" allows every model, including
// models added after the account was saved.
func (u *User) CanUse(modelID string) bool {
	if u.IsAdmin {
		return true
	}
	for _, allowed := range u.AllowedModels {
		if allowed == "*" || allowed == modelID {
			return true
		}
	}
	return false
}

// Config is one immutable snapshot of the gateway configuration.
type Config struct {
	Providers  map[string]*Provider
	Models     map[string]*Model
	UsersByKey map[string]*User
	UsersByID  map[string]*User
	// DropParams removes null values and litellm_/_-prefixed fields before a
	// request is forwarded. It is a compatibility switch for the internal
	// settings table.
	DropParams bool
	// SelfRegistration allows console visitors to create their own member
	// accounts. The first administrator is always created through registration.
	SelfRegistration bool
	// HasAdmin reports whether the snapshot contains an administrator account.
	// It is derived while the snapshot loads so console handlers never scan the
	// user table per request.
	HasAdmin bool
	// DefaultAllowedModels is the model allow-list assigned to accounts created
	// through self-registration.
	DefaultAllowedModels []string
}

// AuthEnabled reports whether API keys are required.
func (c *Config) AuthEnabled() bool {
	return len(c.UsersByKey) > 0
}

// Editable representations, as accepted and returned by the console API. They
// carry pointers where a zero value would be indistinguishable from "unset".

// ProviderInput is the editable representation of a provider.
type ProviderInput struct {
	ID             string                           `json:"id"`
	Name           string                           `json:"name"`
	BaseURL        string                           `json:"base_url"`
	APIKey         string                           `json:"api_key"`
	MaxRetries     *int                             `json:"max_retries"`
	Timeout        *float64                         `json:"timeout"`
	Endpoints      map[string]ProviderEndpointInput `json:"endpoints"`
	Headers        map[string]string                `json:"headers"`
	Proxy          string                           `json:"proxy"`
	UpstreamModels []string                         `json:"upstream_models"`
}

// ProviderEndpointInput configures one gateway endpoint on a provider. Path is
// optional and defaults to the endpoint's canonical upstream path. Converter
// selects the transformation applied to the upstream request and response;
// leave it empty or set it to "none" to forward the inbound wire format.
type ProviderEndpointInput struct {
	Path      string `json:"path,omitempty"`
	Converter string `json:"converter,omitempty"`
}

// ModelProviderInput binds a model to one upstream provider. UpstreamModel is the
// provider's own model name; the gateway serves it under the alias in
// ModelInput.ID.
type ModelProviderInput struct {
	ProviderID    string   `json:"provider_id"`
	UpstreamModel string   `json:"upstream_model"`
	MaxRetryAfter *float64 `json:"max_retry_after"`
}

// ModelInput is the editable representation of a model.
type ModelInput struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Providers        []ModelProviderInput `json:"providers"`
	Routing          string               `json:"routing"`
	ContextLength    *int                 `json:"context_length"`
	MaxOutputLength  *int                 `json:"max_output_length"`
	Modalities       []string             `json:"modalities"`
	OutputModalities []string             `json:"output_modalities"`
	Reasoning        bool                 `json:"reasoning"`
	Tools            bool                 `json:"tools"`
	Timeout          *float64             `json:"timeout"`
}

// UserInput is the editable representation of a console account. Password is
// accepted on write and never returned; KeyCount is reported on read.
type UserInput struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Password      string   `json:"password,omitempty"`
	AllowedModels []string `json:"allowed_models"`
	IsAdmin       bool     `json:"is_admin"`
	KeyCount      int      `json:"key_count"`
}

// APIKeyInput describes one gateway credential owned by a user.
type APIKeyInput struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	CreatedAt string `json:"created_at"`
}
