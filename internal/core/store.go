package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// DefaultDatabasePath is used when --db is not supplied.
const DefaultDatabasePath = "llm-proxy.db"

// Column lists shared by every read of a table. The order must match the scan
// functions below.
const (
	providerFields = `id, name, base_url, api_key, max_retries, timeout, endpoints, headers, proxy, upstream_models`
	modelFields    = `id, name, routing, context_length, max_output_length, modalities, output_modalities, reasoning, tools, timeout`
	userFields     = `id, name, allowed_models, is_admin, password_hash`
	userListFields = `u.id, u.name, u.allowed_models, u.is_admin,
		(SELECT COUNT(*) FROM api_keys k WHERE k.user_id = u.id)`
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("record not found")

// ErrAdminExists is returned when administrator registration is attempted after
// the first administrator has already been created.
var ErrAdminExists = errors.New("an administrator account already exists")

// ErrUserIDTaken is returned when an account registration reuses an existing id.
var ErrUserIDTaken = errors.New("user id is already in use")

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

// Store owns the SQLite database. All configuration is loaded from here; there
// is no configuration file.
type Store struct {
	db   *sql.DB
	path string
}

// OpenStore opens (and creates when necessary) the SQLite database.
func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultDatabasePath
	}
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
		}
		// The database holds provider secrets and API keys. Create a new file
		// with owner-only permissions; an existing file keeps its own mode.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		switch {
		case err == nil:
			if err := file.Close(); err != nil {
				return nil, fmt.Errorf("create database file: %w", err)
			}
		case !errors.Is(err, os.ErrExist):
			return nil, fmt.Errorf("create database file: %w", err)
		}
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite allows a single writer. A single connection also keeps in-memory
	// databases consistent in tests.
	db.SetMaxOpenConns(1)
	// A quick sanity check that the connection opens with the DSN pragmas, so a
	// malformed path fails here instead of on the first query.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}
	store := &Store{db: db, path: path}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// sqliteDSN attaches the connection pragmas to the database path. They must be
// part of the DSN rather than a one-off Exec: database/sql discards a broken
// connection and opens a replacement without replaying it, which would silently
// drop foreign keys, WAL journaling and the busy timeout.
func sqliteDSN(path string) string {
	// A path that already carries a query string (or a full SQLite URI) is
	// passed through untouched; the pragmas below cannot be merged into it
	// without rewriting the caller's URI.
	if strings.Contains(path, "?") || strings.HasPrefix(path, "file:") {
		return path
	}
	params := url.Values{}
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "busy_timeout(5000)")
	return path + "?" + params.Encode()
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// Path returns the database path.
func (s *Store) Path() string { return s.path }

// SetPassword replaces a user's password verifier.
func (s *Store) SetPassword(userID, password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	encoded, err := hashPassword(password)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		encoded, time.Now().UTC().Format(time.RFC3339Nano), userID)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

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
	for wire, endpoint := range endpoints {
		out[string(wire)] = ProviderEndpointInput{
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
