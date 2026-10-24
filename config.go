package llmproxy

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ratsdev/llm-proxy/util"
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

// Provider is one upstream endpoint group.
type Provider struct {
	ID         string
	BaseURL    string
	Protocol   ProviderProtocol
	Key        string
	MaxRetries int
	Timeout    *float64
	Endpoints  []string
	Headers    map[string]string
	Proxy      string
}

// URLFor joins the base URL with an endpoint path.
func (p *Provider) URLFor(endpoint string) string {
	return p.BaseURL + endpoint
}

// OutboundFor picks the declared endpoint to use for an operation.
//
// Providers are selected by weight irrespective of their endpoints. Once a
// provider is chosen, its endpoint is matched to the inbound wire format when
// possible, falling back to the first declared chat-family endpoint.
func (p *Provider) OutboundFor(operation string, preferred WireFormat) (endpoint string, wire WireFormat, ok bool) {
	var fallback routeSpec
	fallbackEndpoint := ""
	for _, candidate := range p.Endpoints {
		spec, exists := routeSpecForOperation(candidate, operation)
		if !exists {
			continue
		}
		if spec.wire == preferred {
			return candidate, spec.wire, true
		}
		if fallbackEndpoint == "" {
			fallback, fallbackEndpoint = spec, candidate
		}
	}
	if fallbackEndpoint == "" {
		return "", "", false
	}
	return fallbackEndpoint, fallback.wire, true
}

// ModelProvider binds a model to one provider's upstream model name.
type ModelProvider struct {
	ProviderID    string
	UpstreamModel string
	Weight        float64
}

// Model is a caller-visible model with its upstream providers.
type Model struct {
	ID              string
	Providers       []ModelProvider
	ContextLength   *int
	MaxOutputLength *int
	Modalities      []string
	Timeout         *float64
}

// User is an API-key holder with an optional model allow-list.
type User struct {
	ID            string
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
	General   fileGeneral    `toml:"general"`
	Server    fileServer     `toml:"server"`
	Providers []fileProvider `toml:"providers"`
	Models    []fileModel    `toml:"models"`
	Users     []fileUser     `toml:"users"`
}

type fileGeneral struct {
	DropParams bool   `toml:"drop_params"`
	LogLevel   string `toml:"log_level"`
}

type fileServer struct {
	Host     string `toml:"host"`
	Port     *int   `toml:"port"`
	LogLevel string `toml:"log_level"`
}

type fileProvider struct {
	ID         string            `toml:"id"`
	BaseURL    string            `toml:"base_url"`
	Protocol   string            `toml:"protocol"`
	Key        string            `toml:"key"`
	MaxRetries *int              `toml:"max_retries"`
	Timeout    *float64          `toml:"timeout"`
	Endpoints  []string          `toml:"endpoints"`
	Headers    map[string]string `toml:"headers"`
	Proxy      string            `toml:"proxy"`
}

type fileModel struct {
	ID              string           `toml:"id"`
	Providers       []map[string]any `toml:"providers"`
	ContextLength   *int             `toml:"context_length"`
	MaxOutputLength *int             `toml:"max_output_length"`
	Modalities      []string         `toml:"modalities"`
	Timeout         *float64         `toml:"timeout"`
}

type fileUser struct {
	ID            string   `toml:"id"`
	APIKey        string   `toml:"api_key"`
	AllowedModels []string `toml:"allowed_models"`
}

// LoadConfig reads and validates a TOML configuration file.
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

	var file fileConfig
	meta, err := toml.Decode(string(data), &file)
	if err != nil {
		return nil, fmt.Errorf("invalid TOML in %s: %w", path, decodeConfigError(err))
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("unknown configuration keys in %s: %s", path, strings.Join(keys, ", "))
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

func decodeConfigError(err error) error {
	text := err.Error()
	switch {
	case strings.Contains(text, "max_retries"):
		return fmt.Errorf("max_retries must be an integer between 0 and %d", maxProviderRetries)
	case strings.Contains(text, "timeout"):
		return fmt.Errorf("timeout must be a positive number")
	case strings.Contains(text, "drop_params"):
		return fmt.Errorf("general.drop_params must be a boolean")
	case strings.Contains(text, "port"):
		return fmt.Errorf("server.port must be an integer between 1 and 65535")
	default:
		return err
	}
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
	for index, item := range f.Providers {
		provider, err := item.build(index)
		if err != nil {
			return nil, err
		}
		if _, exists := providers[provider.ID]; exists {
			return nil, fmt.Errorf("duplicate provider id %q", provider.ID)
		}
		providers[provider.ID] = provider
	}

	models := make(map[string]*Model)
	for index, item := range f.Models {
		model, err := item.build(index)
		if err != nil {
			return nil, err
		}
		if _, exists := models[model.ID]; exists {
			return nil, fmt.Errorf("duplicate model id %q", model.ID)
		}
		for _, modelProvider := range model.Providers {
			if _, exists := providers[modelProvider.ProviderID]; !exists {
				return nil, fmt.Errorf("model %q references unknown provider %q", model.ID, modelProvider.ProviderID)
			}
		}
		models[model.ID] = model
	}

	usersByKey := make(map[string]*User)
	userIDs := make(map[string]bool)
	for index, item := range f.Users {
		user, err := item.build(index)
		if err != nil {
			return nil, err
		}
		if userIDs[user.ID] {
			return nil, fmt.Errorf("duplicate user id %q", user.ID)
		}
		userIDs[user.ID] = true
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

func (f fileProvider) build(index int) (*Provider, error) {
	where := fmt.Sprintf("providers[%d]", index)

	id := strings.TrimSpace(f.ID)
	if id == "" {
		return nil, fmt.Errorf("%s.id must be a non-empty string", where)
	}
	baseURL := strings.TrimSpace(f.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("%s.base_url must be a non-empty string", where)
	}
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("%s.base_url must be an http(s) URL, got %q", where, baseURL)
	}
	if parsedBase.Host == "" || (parsedBase.Scheme != "http" && parsedBase.Scheme != "https") {
		return nil, fmt.Errorf("%s.base_url must be an http(s) URL, got %q", where, baseURL)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	protocol := ProtocolOpenAI
	if f.Protocol != "" {
		candidate := ProviderProtocol(f.Protocol)
		if !slices.Contains(supportedProtocols, candidate) {
			return nil, fmt.Errorf("%s.protocol %v is not supported (expected one of: %s)",
				where, f.Protocol, protocolList())
		}
		protocol = candidate
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
	endpoints, err := normalizeEndpoints(f.Endpoints, where, protocol)
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

	return &Provider{
		ID:         id,
		BaseURL:    baseURL,
		Protocol:   protocol,
		Key:        f.Key,
		MaxRetries: maxRetries,
		Timeout:    timeout,
		Endpoints:  endpoints,
		Headers:    headers,
		Proxy:      proxy,
	}, nil
}

func (f fileModel) build(index int) (*Model, error) {
	where := fmt.Sprintf("models[%d]", index)

	id := strings.TrimSpace(f.ID)
	if id == "" {
		return nil, fmt.Errorf("%s.id must be a non-empty string", where)
	}
	if len(f.Providers) == 0 {
		return nil, fmt.Errorf("%s.providers must be a non-empty array", where)
	}

	providers := make([]ModelProvider, 0, len(f.Providers))
	declared := 0
	for refIndex, raw := range f.Providers {
		providerWhere := fmt.Sprintf("%s.providers[%d]", where, refIndex)
		modelProvider, hasWeight, err := parseModelProvider(raw, providerWhere)
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

	modalities := f.Modalities
	if modalities == nil {
		modalities = []string{"text"}
	}

	return &Model{
		ID:              id,
		Providers:       providers,
		ContextLength:   f.ContextLength,
		MaxOutputLength: f.MaxOutputLength,
		Modalities:      modalities,
		Timeout:         timeout,
	}, nil
}

func (f fileUser) build(index int) (*User, error) {
	where := fmt.Sprintf("users[%d]", index)

	id := strings.TrimSpace(f.ID)
	if id == "" {
		return nil, fmt.Errorf("%s.id must be a non-empty string", where)
	}
	apiKey := strings.TrimSpace(f.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%s.api_key must be a non-empty string", where)
	}
	return &User{ID: id, APIKey: apiKey, AllowedModels: f.AllowedModels}, nil
}

func normalizeEndpoints(items []string, where string, protocol ProviderProtocol) ([]string, error) {
	if items == nil {
		return []string{defaultEndpoint(protocol)}, nil
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s.endpoints must be a non-empty array of strings", where)
	}

	seen := make(map[string]bool)
	endpoints := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("%s.endpoints must contain non-empty strings", where)
		}
		if !strings.HasPrefix(item, "/") {
			item = "/" + item
		}
		if !endpointAllowed(protocol, item) {
			return nil, fmt.Errorf("%s.endpoints contains unsupported endpoint %q for protocol %q",
				where, item, protocol)
		}
		if !seen[item] {
			seen[item] = true
			endpoints = append(endpoints, item)
		}
	}
	return endpoints, nil
}

var modelProviderKeys = []string{"model", "provider_id", "weight"}

func parseModelProvider(raw map[string]any, where string) (ModelProvider, bool, error) {
	unknown := make([]string, 0)
	for key := range raw {
		if !slices.Contains(modelProviderKeys, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return ModelProvider{}, false, fmt.Errorf("%s has unknown keys: %s (expected: %s)",
			where, strings.Join(unknown, ", "), strings.Join(modelProviderKeys, ", "))
	}

	providerID, err := requireString(raw, "provider_id", where)
	if err != nil {
		return ModelProvider{}, false, err
	}
	upstreamModel, err := requireString(raw, "model", where)
	if err != nil {
		return ModelProvider{}, false, err
	}

	weight := 1.0
	value, hasWeight := raw["weight"]
	if hasWeight {
		number, valid := util.AsFloat(value)
		if !valid || number <= 0 {
			return ModelProvider{}, false, fmt.Errorf("%s.weight must be a positive number", where)
		}
		weight = number
	}
	return ModelProvider{
		ProviderID:    providerID,
		UpstreamModel: upstreamModel,
		Weight:        weight,
	}, hasWeight, nil
}

func requireString(raw map[string]any, key, where string) (string, error) {
	value, ok := raw[key]
	if !ok {
		return "", fmt.Errorf("%s.%s must be a non-empty string", where, key)
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s.%s must be a non-empty string", where, key)
	}
	return strings.TrimSpace(text), nil
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

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
