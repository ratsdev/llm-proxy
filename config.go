package llmproxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
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

// Provider is one upstream service. A single provider may expose an OpenAI
// base URL, an Anthropic base URL, or both, and each protocol has its own
// endpoint table.
type Provider struct {
	ID         string
	Name       string
	BaseURLs   map[ProviderProtocol]string
	Key        string
	MaxRetries int
	Timeout    *float64
	Endpoints  map[ProviderProtocol][]ProviderEndpoint
	Headers    map[string]string
	Proxy      string
}

// ProviderEndpoint is one configured upstream endpoint. Name is the
// configuration key ("chat_completions", "responses", "messages", ...), while
// Path is the path appended to the protocol's base URL.
type ProviderEndpoint struct {
	Name string
	Path string
	Wire WireFormat
}

// Protocols returns the configured protocols in their canonical order.
func (p *Provider) Protocols() []ProviderProtocol {
	protocols := make([]ProviderProtocol, 0, len(p.BaseURLs))
	for _, protocol := range supportedProtocols {
		if _, ok := p.BaseURLs[protocol]; ok {
			protocols = append(protocols, protocol)
		}
	}
	return protocols
}

// HasProtocol reports whether the provider has a base URL for the protocol.
func (p *Provider) HasProtocol(protocol ProviderProtocol) bool {
	_, ok := p.BaseURLs[protocol]
	return ok
}

// URLFor joins a protocol's base URL with an endpoint path.
func (p *Provider) URLFor(protocol ProviderProtocol, endpoint string) string {
	return p.BaseURLs[protocol] + endpoint
}

// OutboundFor picks the configured endpoint to use for a request.
//
// Providers are selected by weight irrespective of their endpoints. Once a
// provider is chosen, its endpoint is matched to the inbound wire format when
// possible, falling back to the first compatible endpoint. When protocols are
// supplied, only endpoints from those protocols are considered; the router
// uses that to keep video requests on OpenAI-compatible endpoints.
func (p *Provider) OutboundFor(preferred WireFormat, protocols ...ProviderProtocol) (ProviderEndpoint, ProviderProtocol, bool) {
	var fallback ProviderEndpoint
	var fallbackProtocol ProviderProtocol
	hasFallback := false
	for _, protocol := range p.Protocols() {
		if len(protocols) > 0 && !slices.Contains(protocols, protocol) {
			continue
		}
		for _, candidate := range p.Endpoints[protocol] {
			if candidate.Wire == preferred {
				return candidate, protocol, true
			}
			if !hasFallback {
				fallback, fallbackProtocol, hasFallback = candidate, protocol, true
			}
		}
	}
	return fallback, fallbackProtocol, hasFallback
}

// ModelProvider binds a model to one provider's upstream model name.
type ModelProvider struct {
	ProviderID    string
	UpstreamModel string
	Weight        float64
	// MaxRetryAfter caps the Retry-After wait for this binding in seconds.
	// Nil uses the gateway default.
	MaxRetryAfter *float64
}

// Model is a caller-visible model with its upstream providers.
type Model struct {
	ID               string
	Name             string
	Providers        []ModelProvider
	ContextLength    *int
	MaxOutputLength  *int
	Modalities       []string
	OutputModalities []string
	Timeout          *float64
}

// User is an API-key holder with an optional model allow-list.
type User struct {
	ID            string
	Name          string
	APIKey        string
	AllowedModels []string
}

// CanUse reports whether the user may call a model.
func (u *User) CanUse(modelID string) bool {
	if u.AllowedModels == nil {
		return true
	}
	for _, allowed := range u.AllowedModels {
		if allowed == "*" || allowed == modelID {
			return true
		}
	}
	return false
}

// Config is the immutable gateway configuration.
type Config struct {
	Providers  map[string]*Provider
	Models     map[string]*Model
	UsersByKey map[string]*User
	DropParams bool
	Host       string
	Port       int
	LogLevel   string
}

// AuthEnabled reports whether API keys are required.
func (c *Config) AuthEnabled() bool {
	return len(c.UsersByKey) > 0
}

// Overrides carries command-line values that take precedence over the file.
// Zero values leave the corresponding setting untouched.
type Overrides struct {
	Host     string
	Port     int
	LogLevel string
}

type fileConfig struct {
	General   fileGeneral             `yaml:"general"`
	Server    fileServer              `yaml:"server"`
	Providers map[string]fileProvider `yaml:"providers"`
	Models    map[string]fileModel    `yaml:"models"`
	Users     map[string]fileUser     `yaml:"users"`
}

type fileGeneral struct {
	DropParams bool   `yaml:"drop_params"`
	LogLevel   string `yaml:"log_level"`
}

type fileServer struct {
	Host     string `yaml:"host"`
	Port     *int   `yaml:"port"`
	LogLevel string `yaml:"log_level"`
}

type fileProvider struct {
	Name       string                        `yaml:"name"`
	BaseURLs   map[string]string             `yaml:"base_url"`
	APIKey     string                        `yaml:"api_key"`
	MaxRetries *int                          `yaml:"max_retries"`
	Timeout    *float64                      `yaml:"timeout"`
	Endpoints  map[string]map[string]*string `yaml:"endpoints"`
	Headers    map[string]string             `yaml:"headers"`
	Proxy      string                        `yaml:"proxy"`
}

type fileModel struct {
	Name            string              `yaml:"name"`
	Providers       []fileModelProvider `yaml:"providers"`
	ContextLength   *int                `yaml:"context_length"`
	MaxOutputLength *int                `yaml:"max_output_length"`
	Modalities      *fileModalities     `yaml:"modalities"`
	Timeout         *float64            `yaml:"timeout"`
}

type fileModalities struct {
	Input  []string `yaml:"input"`
	Output []string `yaml:"output"`
}

type fileModelProvider struct {
	ProviderID    string   `yaml:"provider_id"`
	ModelID       string   `yaml:"model_id"`
	Weight        *float64 `yaml:"weight"`
	MaxRetryAfter *float64 `yaml:"max_retry_after"`
}

type fileUser struct {
	Name          string   `yaml:"name"`
	APIKey        string   `yaml:"api_key"`
	AllowedModels []string `yaml:"allowed_models"`
}

// LoadConfig reads and validates a YAML configuration file.
func LoadConfig(path string) (*Config, error) {
	return LoadConfigWith(path, Overrides{})
}

// LoadConfigWith reads a configuration file and applies command-line
// overrides, returning a fully populated immutable Config.
func LoadConfigWith(path string, overrides Overrides) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found: %s", path)
		}
		return nil, fmt.Errorf("cannot read config file %s: %w", path, err)
	}

	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("config file %s is empty", path)
	}

	var file fileConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config file %s is empty", path)
		}
		if keys := unknownConfigKeys(err); len(keys) > 0 {
			return nil, fmt.Errorf("unknown configuration keys in %s: %s", path, strings.Join(keys, ", "))
		}
		return nil, fmt.Errorf("invalid YAML in %s: %w", path, decodeConfigError(err, data))
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, fmt.Errorf("invalid YAML in %s: %w", path, decodeConfigError(err, data))
	default:
		return nil, fmt.Errorf("config file %s must contain a single YAML document", path)
	}
	cfg, err := file.build()
	if err != nil {
		return nil, err
	}
	if overrides.Host != "" {
		cfg.Host = strings.TrimSpace(overrides.Host)
	}
	if overrides.Port != 0 {
		if overrides.Port < 1 || overrides.Port > 65535 {
			return nil, fmt.Errorf("port must be an integer between 1 and 65535")
		}
		cfg.Port = overrides.Port
	}
	if overrides.LogLevel != "" {
		cfg.LogLevel = overrides.LogLevel
	}
	return cfg, nil
}

// unknownConfigKeys extracts the offending keys from a yaml.v3 KnownFields
// error, sorted and deduplicated so the message is stable.
func unknownConfigKeys(err error) []string {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return nil
	}
	keys := make([]string, 0, len(typeErr.Errors))
	for _, entry := range typeErr.Errors {
		if !strings.Contains(entry, "not found in type") {
			continue
		}
		const marker = "field "
		index := strings.Index(entry, marker)
		if index < 0 {
			continue
		}
		rest := entry[index+len(marker):]
		end := strings.IndexByte(rest, ' ')
		if end <= 0 {
			continue
		}
		keys = append(keys, rest[:end])
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

// decodeConfigError turns YAML type errors into messages that name the
// offending configuration field. yaml.v3 reports a source line rather than the
// struct field, so yamlErrorField looks that line up in the original document.
func decodeConfigError(err error, data []byte) error {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return err
	}
	for _, entry := range typeErr.Errors {
		switch yamlErrorField(entry, data) {
		case "max_retries":
			return fmt.Errorf("max_retries must be an integer between 0 and %d", maxProviderRetries)
		case "timeout":
			return fmt.Errorf("timeout must be a positive number")
		case "max_retry_after":
			return fmt.Errorf("max_retry_after must be a non-negative number")
		case "drop_params":
			return fmt.Errorf("general.drop_params must be a boolean")
		case "port":
			return fmt.Errorf("server.port must be an integer between 1 and 65535")
		}
	}
	return err
}

// yamlErrorField resolves the configuration key on the source line a yaml.v3
// TypeError points at. Block scalar values sit below their key, so the search
// walks up until it finds a line that contains one.
func yamlErrorField(entry string, data []byte) string {
	number := yamlErrorLine(entry)
	lines := strings.Split(string(data), "\n")
	if number < 1 || number > len(lines) {
		return ""
	}
	for index := number - 1; index >= 0; index-- {
		source := strings.TrimSpace(lines[index])
		if source == "" || strings.HasPrefix(source, "#") {
			continue
		}
		source = strings.TrimPrefix(source, "- ")
		colon := strings.Index(source, ":")
		if colon < 0 {
			continue
		}
		return strings.Trim(strings.TrimSpace(source[:colon]), `"'`)
	}
	return ""
}

func yamlErrorLine(entry string) int {
	const marker = "line "
	index := strings.Index(entry, marker)
	if index < 0 {
		return 0
	}
	rest := entry[index+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	number, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return number
}

func (f fileConfig) build() (*Config, error) {
	host := "0.0.0.0"
	if trimmed := strings.TrimSpace(f.Server.Host); trimmed != "" {
		host = trimmed
	}
	port := 4000
	if f.Server.Port != nil {
		if *f.Server.Port < 1 || *f.Server.Port > 65535 {
			return nil, fmt.Errorf("server.port must be an integer between 1 and 65535")
		}
		port = *f.Server.Port
	}
	logLevel := "info"
	switch {
	case strings.TrimSpace(f.Server.LogLevel) != "":
		logLevel = strings.TrimSpace(f.Server.LogLevel)
	case strings.TrimSpace(f.General.LogLevel) != "":
		logLevel = strings.TrimSpace(f.General.LogLevel)
	}

	providers := make(map[string]*Provider)
	for _, id := range sortedMapKeys(f.Providers) {
		provider, err := f.Providers[id].build(id)
		if err != nil {
			return nil, err
		}
		providers[provider.ID] = provider
	}

	models := make(map[string]*Model)
	for _, id := range sortedMapKeys(f.Models) {
		model, err := f.Models[id].build(id)
		if err != nil {
			return nil, err
		}
		for _, modelProvider := range model.Providers {
			if _, exists := providers[modelProvider.ProviderID]; !exists {
				return nil, fmt.Errorf("model %q references unknown provider %q", model.ID, modelProvider.ProviderID)
			}
		}
		models[model.ID] = model
	}

	usersByKey := make(map[string]*User)
	for _, id := range sortedMapKeys(f.Users) {
		user, err := f.Users[id].build(id)
		if err != nil {
			return nil, err
		}
		if _, exists := usersByKey[user.APIKey]; exists {
			return nil, fmt.Errorf("duplicate api_key for user %q", user.ID)
		}
		for _, allowed := range user.AllowedModels {
			if allowed == "*" {
				continue
			}
			if _, exists := models[allowed]; !exists {
				return nil, fmt.Errorf("user %q allows unknown models: %s", user.ID, allowed)
			}
		}
		usersByKey[user.APIKey] = user
	}

	return &Config{
		Providers:  providers,
		Models:     models,
		UsersByKey: usersByKey,
		DropParams: f.General.DropParams,
		Host:       host,
		Port:       port,
		LogLevel:   logLevel,
	}, nil
}

func (f fileProvider) build(id string) (*Provider, error) {
	id, err := configID("provider", id)
	if err != nil {
		return nil, err
	}
	where := fmt.Sprintf("providers.%s", id)

	baseURLs, err := normalizeBaseURLs(f.BaseURLs, where)
	if err != nil {
		return nil, err
	}

	maxRetries := 2
	if f.MaxRetries != nil {
		if *f.MaxRetries < 0 || *f.MaxRetries > maxProviderRetries {
			return nil, fmt.Errorf("%s.max_retries must be an integer between 0 and %d", where, maxProviderRetries)
		}
		maxRetries = *f.MaxRetries
	}

	timeout, err := validateTimeout(f.Timeout, where)
	if err != nil {
		return nil, err
	}
	endpoints, err := normalizeProviderEndpoints(f.Endpoints, baseURLs, where)
	if err != nil {
		return nil, err
	}
	proxy, err := validateProxy(f.Proxy, where)
	if err != nil {
		return nil, err
	}

	headers := f.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	if err := validateHeaders(headers, where); err != nil {
		return nil, err
	}

	return &Provider{
		ID:         id,
		Name:       strings.TrimSpace(f.Name),
		BaseURLs:   baseURLs,
		Key:        strings.TrimSpace(f.APIKey),
		MaxRetries: maxRetries,
		Timeout:    timeout,
		Endpoints:  endpoints,
		Headers:    headers,
		Proxy:      proxy,
	}, nil
}

func (f fileModel) build(id string) (*Model, error) {
	id, err := configID("model", id)
	if err != nil {
		return nil, err
	}
	where := fmt.Sprintf("models.%s", id)

	if len(f.Providers) == 0 {
		return nil, fmt.Errorf("%s.providers must be a non-empty array", where)
	}

	providers := make([]ModelProvider, 0, len(f.Providers))
	declared := 0
	for refIndex, raw := range f.Providers {
		providerWhere := fmt.Sprintf("%s.providers[%d]", where, refIndex)
		modelProvider, hasWeight, err := raw.build(providerWhere)
		if err != nil {
			return nil, err
		}
		if hasWeight {
			declared++
		}
		providers = append(providers, modelProvider)
	}
	if declared != 0 {
		if declared != len(providers) {
			return nil, fmt.Errorf("%s.providers must declare a weight for every provider or for none of them", where)
		}
		total := 0.0
		for _, provider := range providers {
			total += provider.Weight
		}
		if math.Abs(total-1.0) > 1e-6 {
			return nil, fmt.Errorf("%s.providers weights must sum to 1.0, got %g", where, total)
		}
	}

	if err := validatePositiveInt(f.ContextLength, "context_length", where); err != nil {
		return nil, err
	}
	if err := validatePositiveInt(f.MaxOutputLength, "max_output_length", where); err != nil {
		return nil, err
	}
	timeout, err := validateTimeout(f.Timeout, where)
	if err != nil {
		return nil, err
	}

	modalities := []string{"text"}
	outputModalities := []string{"text"}
	if f.Modalities != nil {
		if len(f.Modalities.Input) > 0 {
			modalities = append([]string(nil), f.Modalities.Input...)
		}
		if len(f.Modalities.Output) > 0 {
			outputModalities = append([]string(nil), f.Modalities.Output...)
		}
	}

	return &Model{
		ID:               id,
		Name:             strings.TrimSpace(f.Name),
		Providers:        providers,
		ContextLength:    f.ContextLength,
		MaxOutputLength:  f.MaxOutputLength,
		Modalities:       modalities,
		OutputModalities: outputModalities,
		Timeout:          timeout,
	}, nil
}

func (f fileModelProvider) build(where string) (ModelProvider, bool, error) {
	providerID := strings.TrimSpace(f.ProviderID)
	if providerID == "" {
		return ModelProvider{}, false, fmt.Errorf("%s.provider_id must be a non-empty string", where)
	}
	upstreamModel := strings.TrimSpace(f.ModelID)
	if upstreamModel == "" {
		return ModelProvider{}, false, fmt.Errorf("%s.model_id must be a non-empty string", where)
	}

	weight := 1.0
	hasWeight := f.Weight != nil
	if hasWeight {
		if !isFinite(*f.Weight) || *f.Weight <= 0 {
			return ModelProvider{}, false, fmt.Errorf("%s.weight must be a positive number", where)
		}
		weight = *f.Weight
	}
	maxRetryAfter, err := validateMaxRetryAfter(f.MaxRetryAfter, where)
	if err != nil {
		return ModelProvider{}, false, err
	}
	return ModelProvider{
		ProviderID:    providerID,
		UpstreamModel: upstreamModel,
		Weight:        weight,
		MaxRetryAfter: maxRetryAfter,
	}, hasWeight, nil
}

func (f fileUser) build(id string) (*User, error) {
	id, err := configID("user", id)
	if err != nil {
		return nil, err
	}
	where := fmt.Sprintf("users.%s", id)
	apiKey := strings.TrimSpace(f.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%s.api_key must be a non-empty string", where)
	}
	return &User{ID: id, Name: strings.TrimSpace(f.Name), APIKey: apiKey, AllowedModels: f.AllowedModels}, nil
}

func normalizeBaseURLs(raw map[string]string, where string) (map[ProviderProtocol]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s.base_url must declare at least one protocol (%s)", where, protocolList())
	}
	baseURLs := make(map[ProviderProtocol]string, len(raw))
	for _, key := range sortedMapKeys(raw) {
		protocol, err := protocolKey(key, where+".base_url")
		if err != nil {
			return nil, err
		}
		baseURL := strings.TrimSpace(raw[key])
		if baseURL == "" {
			return nil, fmt.Errorf("%s.base_url.%s must be a non-empty string", where, protocol)
		}
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("%s.base_url.%s must be an http(s) URL, got %q", where, protocol, baseURL)
		}
		baseURLs[protocol] = strings.TrimRight(baseURL, "/")
	}
	return baseURLs, nil
}

// normalizeProviderEndpoints turns the configuration's endpoint table into the
// provider's immutable endpoint list.
//
// Every configured protocol starts with its default endpoints. Declaring
// endpoints overrides that protocol's list with an allow-list: only the listed
// endpoints are enabled, and a missing or empty path uses the default path.
// Protocols left out of the endpoints table keep every default endpoint.
func normalizeProviderEndpoints(raw map[string]map[string]*string, baseURLs map[ProviderProtocol]string, where string) (map[ProviderProtocol][]ProviderEndpoint, error) {
	endpoints := make(map[ProviderProtocol][]ProviderEndpoint, len(baseURLs))
	for _, protocol := range supportedProtocols {
		if _, exists := baseURLs[protocol]; exists {
			endpoints[protocol] = defaultProviderEndpoints(protocol)
		}
	}
	if raw == nil {
		return endpoints, nil
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s.endpoints must declare at least one endpoint (omit the field to enable every default endpoint)", where)
	}

	for _, key := range sortedMapKeys(raw) {
		protocol, err := protocolKey(key, where+".endpoints")
		if err != nil {
			return nil, err
		}
		if _, exists := baseURLs[protocol]; !exists {
			return nil, fmt.Errorf("%s.endpoints.%s requires base_url.%s", where, protocol, protocol)
		}
		entries := raw[key]
		if len(entries) == 0 {
			return nil, fmt.Errorf("%s.endpoints.%s must be a non-empty mapping (omit endpoints to enable every default endpoint)",
				where, protocol)
		}

		for _, name := range sortedMapKeys(entries) {
			if name != strings.TrimSpace(name) {
				return nil, fmt.Errorf("%s.endpoints.%s %q must not have leading or trailing whitespace",
					where, protocol, name)
			}
			spec, ok := routeSpecForConfigKey(name)
			if !ok {
				return nil, fmt.Errorf("%s.endpoints.%s.%s is not a supported endpoint (expected one of: %s)",
					where, protocol, name, endpointList(protocol))
			}
			if spec.protocol != protocol {
				return nil, fmt.Errorf("%s.endpoints.%s.%s is only supported for protocol %q",
					where, protocol, name, spec.protocol)
			}
		}

		built := make([]ProviderEndpoint, 0, len(entries))
		for _, spec := range routeSpecsForProtocol(protocol) {
			value, ok := entries[spec.configKey]
			if !ok {
				continue
			}
			path := spec.endpoint
			if value != nil {
				if trimmed := strings.TrimSpace(*value); trimmed != "" {
					path, err = endpointPath(trimmed, fmt.Sprintf("%s.endpoints.%s.%s", where, protocol, spec.configKey))
					if err != nil {
						return nil, err
					}
				}
			}
			built = append(built, endpointFromSpec(spec, path))
		}
		endpoints[protocol] = built
	}
	return endpoints, nil
}

func defaultProviderEndpoints(protocol ProviderProtocol) []ProviderEndpoint {
	specs := defaultRouteSpecs(protocol)
	endpoints := make([]ProviderEndpoint, 0, len(specs))
	for _, spec := range specs {
		endpoints = append(endpoints, endpointFromSpec(spec, spec.endpoint))
	}
	return endpoints
}

func endpointFromSpec(spec routeSpec, path string) ProviderEndpoint {
	return ProviderEndpoint{
		Name: spec.configKey,
		Path: path,
		Wire: spec.wire,
	}
}

// endpointPath validates a configured upstream path. Paths are appended to the
// protocol's base URL verbatim, so a full URL, query string, fragment, or
// whitespace would silently change the target instead of failing at startup.
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

// configID validates a map key used as an entity ID. YAML keeps leading and
// trailing whitespace inside quoted keys, so reject those keys instead of
// trimming them into silent duplicates.
func configID(kind, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("%s ids must be non-empty strings", kind)
	}
	if id != strings.TrimSpace(id) {
		return "", fmt.Errorf("%s id %q must not have leading or trailing whitespace", kind, id)
	}
	return id, nil
}

// protocolKey validates a base_url or endpoints key. YAML keeps whitespace in
// quoted keys, so reject it rather than trimming it into a duplicate.
func protocolKey(key, where string) (ProviderProtocol, error) {
	if key != strings.TrimSpace(key) {
		return "", fmt.Errorf("%s %q must not have leading or trailing whitespace", where, key)
	}
	protocol := ProviderProtocol(key)
	if !slices.Contains(supportedProtocols, protocol) {
		return "", fmt.Errorf("%s %q is not supported (expected one of: %s)", where, key, protocolList())
	}
	return protocol, nil
}

func endpointList(protocol ProviderProtocol) string {
	specs := routeSpecsForProtocol(protocol)
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.configKey)
	}
	return strings.Join(names, ", ")
}

func validateTimeout(value *float64, where string) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	if !isFinite(*value) || *value <= 0 {
		return nil, fmt.Errorf("%s.timeout must be a positive number", where)
	}
	if *value > maxTimeoutSeconds {
		return nil, fmt.Errorf("%s.timeout is too large", where)
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
		return nil, fmt.Errorf("%s.max_retry_after must be a non-negative number", where)
	}
	if *value > maxTimeoutSeconds {
		return nil, fmt.Errorf("%s.max_retry_after is too large", where)
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
		return "", fmt.Errorf("%s.proxy must be an http, https, socks5, or socks5h URL, got %q", where, text)
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
// refuse at request time, so a typo fails at startup instead.
func validateHeaders(headers map[string]string, where string) error {
	for _, name := range sortedMapKeys(headers) {
		value := headers[name]
		if name == "" || name != strings.TrimSpace(name) || strings.ContainsFunc(name, invalidHeaderToken) {
			return fmt.Errorf("%s.headers %q is not a valid header name", where, name)
		}
		if strings.ContainsFunc(value, func(r rune) bool {
			return r != '\t' && unicode.IsControl(r)
		}) {
			return fmt.Errorf("%s.headers %q must not contain control characters", where, name)
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
