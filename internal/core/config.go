package core

import (
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

// maxProviderRetries bounds the retry loop so a configuration typo cannot turn
// into an unbounded burst of upstream requests.
const maxProviderRetries = 10

// maxTimeoutSeconds keeps timeout conversions inside time.Duration's int64
// nanosecond range. The integer division deliberately stays one second below
// the exact float boundary, where rounding can overflow the conversion.
const maxTimeoutSeconds = float64(math.MaxInt64 / int64(time.Second))

var proxySchemes = map[string]bool{
	"http": true, "https": true, "socks5": true, "socks5h": true,
}

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
	for _, wire := range WireFormats {
		if _, ok := p.Endpoints[wire]; ok {
			names = append(names, string(wire))
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

// normalizeProviderEndpoints validates the endpoint table and resolves the
// default path for each configured endpoint. At least one endpoint is
// required; endpoints left out are not served by the provider.
func normalizeProviderEndpoints(raw map[string]ProviderEndpointInput, where string) (map[WireFormat]ProviderEndpoint, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s.endpoints must declare at least one endpoint (%s)", where, WireFormatList())
	}
	endpoints := make(map[WireFormat]ProviderEndpoint, len(raw))
	for _, key := range sortedMapKeys(raw) {
		format, err := wireKey(key, where+".endpoints")
		if err != nil {
			return nil, err
		}
		input := raw[key]
		spec, _ := RouteSpecForWire(format)
		path := strings.TrimSpace(input.Path)
		if path == "" {
			path = spec.Endpoint
		}
		path, err = endpointPath(path, fmt.Sprintf("%s.endpoints.%s.path", where, format))
		if err != nil {
			return nil, err
		}
		converter, err := converterID(input.Converter, format, fmt.Sprintf("%s.endpoints.%s.converter", where, format))
		if err != nil {
			return nil, err
		}
		endpoints[format] = ProviderEndpoint{Path: path, Converter: converter}
	}
	return endpoints, nil
}

// normalizeBaseURL validates the base URL shared by a provider's endpoints.
func normalizeBaseURL(raw, where string) (string, error) {
	baseURL := strings.TrimSpace(raw)
	if baseURL == "" {
		return "", fmt.Errorf("%s must be a non-empty http(s) URL", where)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("%s must be an http(s) URL, got %q", where, baseURL)
	}
	return strings.TrimRight(baseURL, "/"), nil
}

// converterID validates an endpoint converter and checks that it starts from
// the endpoint's inbound format.
func converterID(raw string, inbound WireFormat, where string) (string, error) {
	definition, err := convert.DefinitionFor(inbound, raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", where, err)
	}
	return definition.ID, nil
}

// endpointPath validates a configured upstream path. Paths are appended to the
// endpoint's base URL verbatim, so a full URL, query string, fragment, or
// whitespace would silently change the target instead of failing when saved.
func endpointPath(path, where string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("%s must be a path starting with \"/\", got %q", where, path)
	}
	if strings.ContainsFunc(path, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", fmt.Errorf("%s must not contain whitespace or control characters, got %q", where, path)
	}
	parsed, err := url.Parse(path)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must be a URL path without a scheme, host, query, or fragment, got %q", where, path)
	}
	return path, nil
}

// configID validates an entity ID. Leading and trailing whitespace is rejected
// instead of trimmed so two IDs cannot collapse into the same record, control
// characters are rejected because IDs end up in logs, URLs and headers, and
// invalid UTF-8 is rejected because JSON encoding would silently replace it with
// U+FFFD and the stored ID would no longer match the configured one.
func configID(kind, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("%s ids must be non-empty strings", kind)
	}
	if id != strings.TrimSpace(id) {
		return "", fmt.Errorf("%s id %q must not have leading or trailing whitespace", kind, id)
	}
	if !utf8.ValidString(id) {
		return "", fmt.Errorf("%s id %q must be valid UTF-8", kind, id)
	}
	if strings.ContainsFunc(id, unicode.IsControl) {
		return "", fmt.Errorf("%s id %q must not contain control characters", kind, id)
	}
	return id, nil
}

// ValidateUserID reports whether id can be used as a console account
// identifier. Registration handlers call it so an unusable name is rejected as
// bad input instead of surfacing as a store failure.
func ValidateUserID(id string) error {
	_, err := configID("user", id)
	return err
}

// wireKey validates an endpoint key.
func wireKey(key, where string) (WireFormat, error) {
	if key != strings.TrimSpace(key) {
		return "", fmt.Errorf("%s %q must not have leading or trailing whitespace", where, key)
	}
	format := WireFormat(key)
	if !slices.Contains(WireFormats, format) {
		return "", fmt.Errorf("%s %q is not supported (expected one of: %s)", where, key, WireFormatList())
	}
	return format, nil
}

func validateTimeout(value *float64, where string) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	if !isFinite(*value) || *value <= 0 {
		return nil, fmt.Errorf("%s must be a positive number", where)
	}
	if *value > maxTimeoutSeconds {
		return nil, fmt.Errorf("%s is too large", where)
	}
	return value, nil
}

// validateMaxRetryAfter validates the per-binding Retry-After cap in seconds.
// Zero disables waiting, which makes the gateway retry without a pause.
func validateMaxRetryAfter(value *float64, where string) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	if !isFinite(*value) || *value < 0 {
		return nil, fmt.Errorf("%s must be a non-negative number", where)
	}
	if *value > maxTimeoutSeconds {
		return nil, fmt.Errorf("%s is too large", where)
	}
	return value, nil
}

func validatePositiveInt(value *int, key, where string) error {
	if value == nil {
		return nil
	}
	if *value <= 0 {
		return fmt.Errorf("%s.%s must be a positive integer", where, key)
	}
	return nil
}

func validateProxy(text, where string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	parsed, err := url.Parse(text)
	if err != nil || !proxySchemes[parsed.Scheme] || parsed.Host == "" {
		return "", fmt.Errorf("%s must be an http, https, socks5, or socks5h URL, got %q", where, text)
	}
	return text, nil
}

// invalidHeaderToken reports characters that cannot appear in an HTTP header
// name. Go's transport rejects these only when sending, which would surface as
// an upstream failure instead of a configuration error.
func invalidHeaderToken(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r)
}

// validateHeaders rejects header names and values that Go's transport would
// refuse at request time, so a typo fails when the record is saved instead.
func validateHeaders(headers map[string]string, where string) error {
	for _, name := range sortedMapKeys(headers) {
		value := headers[name]
		if name == "" || name != strings.TrimSpace(name) || strings.ContainsFunc(name, invalidHeaderToken) {
			return fmt.Errorf("%s %q is not a valid header name", where, name)
		}
		if strings.ContainsFunc(value, func(r rune) bool {
			return r != '\t' && unicode.IsControl(r)
		}) {
			return fmt.Errorf("%s %q must not contain control characters", where, name)
		}
	}
	return nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func sortedMapKeys[V any](items map[string]V) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
