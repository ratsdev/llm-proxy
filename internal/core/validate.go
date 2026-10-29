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
