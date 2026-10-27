package core

import (
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

func (s *Store) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS providers (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL DEFAULT '',
			base_url    TEXT NOT NULL DEFAULT '',
			api_key     TEXT NOT NULL DEFAULT '',
			max_retries INTEGER NOT NULL DEFAULT 2,
			timeout     REAL,
			endpoints   TEXT NOT NULL DEFAULT '{}',
			headers     TEXT NOT NULL DEFAULT '{}',
			proxy       TEXT NOT NULL DEFAULT '',
			upstream_models TEXT NOT NULL DEFAULT '[]'
		)`,
		`CREATE TABLE IF NOT EXISTS models (
			id                TEXT PRIMARY KEY,
			name              TEXT NOT NULL DEFAULT '',
			routing           TEXT NOT NULL DEFAULT 'priority',
			context_length    INTEGER,
			max_output_length INTEGER,
			modalities        TEXT NOT NULL DEFAULT '["text"]',
			output_modalities TEXT NOT NULL DEFAULT '["text"]',
			reasoning         INTEGER NOT NULL DEFAULT 0,
			tools             INTEGER NOT NULL DEFAULT 0,
			timeout           REAL
		)`,
		`CREATE TABLE IF NOT EXISTS model_providers (
			model_id       TEXT NOT NULL,
			provider_id    TEXT NOT NULL,
			upstream_model TEXT NOT NULL,
			max_retry_after REAL,
			position       INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (model_id, provider_id, upstream_model),
			FOREIGN KEY (model_id) REFERENCES models(id) ON DELETE CASCADE,
			FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE RESTRICT
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id            TEXT PRIMARY KEY,
			name          TEXT NOT NULL DEFAULT '',
			allowed_models TEXT NOT NULL DEFAULT '[]',
			is_admin      INTEGER NOT NULL DEFAULT 0,
			password_hash TEXT NOT NULL DEFAULT '',
			created_at    TEXT NOT NULL,
			updated_at    TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			key        TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS api_keys_user_id ON api_keys(user_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
	}
	if err := s.migrateProviderEndpoints(); err != nil {
		return err
	}
	if err := s.migrateEndpointConverters(); err != nil {
		return err
	}
	if err := s.migrateModelCapabilities(); err != nil {
		return err
	}
	return s.migrateUserAPIKeys()
}

// migrateUserAPIKeys moves the single legacy users.api_key value into the
// api_keys table, then drops the column. Each account can own many keys, so
// the old one-key column no longer exists.
func (s *Store) migrateUserAPIKeys() error {
	columns, err := s.tableColumns("users")
	if err != nil {
		return err
	}
	if !columns["api_key"] {
		return nil
	}
	rows, err := s.db.Query(`SELECT id, api_key FROM users WHERE api_key IS NOT NULL AND api_key <> ''`)
	if err != nil {
		return fmt.Errorf("migrate users: read legacy api keys: %w", err)
	}
	type legacyKey struct {
		userID string
		key    string
	}
	legacy := make([]legacyKey, 0)
	for rows.Next() {
		var entry legacyKey
		if err := rows.Scan(&entry.userID, &entry.key); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate users: scan legacy api key: %w", err)
		}
		legacy = append(legacy, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("migrate users: read legacy api keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, entry := range legacy {
		if _, err := s.db.Exec(`
			INSERT OR IGNORE INTO api_keys (id, user_id, name, key, created_at)
			VALUES (?, ?, 'Default', ?, ?)`,
			"legacy-"+entry.userID, entry.userID, entry.key, now); err != nil {
			return fmt.Errorf("migrate users: move api key for %q: %w", entry.userID, err)
		}
	}
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS users_api_key_unique`); err != nil {
		return fmt.Errorf("migrate users: drop api key index: %w", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE users DROP COLUMN api_key`); err != nil {
		return fmt.Errorf("migrate users: drop api_key: %w", err)
	}
	return nil
}

// migrateModelCapabilities adds the columns introduced after the first release.
// SQLite has no "ADD COLUMN IF NOT EXISTS", so the table metadata decides.
func (s *Store) migrateModelCapabilities() error {
	providerColumns, err := s.tableColumns("providers")
	if err != nil {
		return err
	}
	if !providerColumns["upstream_models"] {
		if _, err := s.db.Exec(`ALTER TABLE providers ADD COLUMN upstream_models TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return fmt.Errorf("migrate providers: add upstream_models: %w", err)
		}
	}
	modelColumns, err := s.tableColumns("models")
	if err != nil {
		return err
	}
	for column, definition := range map[string]string{
		"routing":   "TEXT NOT NULL DEFAULT '" + RoutingPriority + "'",
		"reasoning": "INTEGER NOT NULL DEFAULT 0",
		"tools":     "INTEGER NOT NULL DEFAULT 0",
	} {
		if modelColumns[column] {
			continue
		}
		if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE models ADD COLUMN %s %s`, column, definition)); err != nil {
			return fmt.Errorf("migrate models: add %s: %w", column, err)
		}
	}
	bindingColumns, err := s.tableColumns("model_providers")
	if err != nil {
		return err
	}
	if bindingColumns["weight"] {
		if _, err := s.db.Exec(`ALTER TABLE model_providers DROP COLUMN weight`); err != nil {
			return fmt.Errorf("migrate model_providers: drop weight: %w", err)
		}
	}
	return nil
}

func (s *Store) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.db.Query(fmt.Sprintf(`SELECT name FROM pragma_table_info('%s')`, table))
	if err != nil {
		return nil, fmt.Errorf("inspect %s table: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("inspect %s table: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect %s table: %w", table, err)
	}
	return columns, nil
}

// migrateProviderEndpoints rewrites provider rows created by older schema
// versions. Two layouts existed before endpoints shared one base URL:
//   - a base_urls column keyed by vendor protocol, and
//   - a per-endpoint base_url inside the endpoints JSON.
//
// Both are converted to a shared provider base_url plus endpoint path and
// converter field.
func (s *Store) migrateProviderEndpoints() error {
	columns, err := s.tableColumns("providers")
	if err != nil {
		return err
	}
	if columns["base_urls"] {
		return s.migrateLegacyBaseURLs()
	}
	if !columns["base_url"] {
		if _, err := s.db.Exec(`ALTER TABLE providers ADD COLUMN base_url TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate providers: add base_url: %w", err)
		}
	}
	return s.migratePerEndpointBaseURLs()
}

// storedProviderEndpoint is the endpoint shape written by every earlier schema
// version. Each migration reads the fields it needs and writes the current
// ProviderEndpointInput back.
type storedProviderEndpoint struct {
	BaseURL   string `json:"base_url"`
	Path      string `json:"path"`
	Protocol  string `json:"protocol"`
	Converter string `json:"converter"`
}

// rewriteResult is what one migration wants to store for a provider.
type rewriteResult struct {
	// BaseURL replaces the shared base URL when non-nil.
	BaseURL *string
	// Endpoints replaces the endpoint table.
	Endpoints map[string]ProviderEndpointInput
	// Changed reports whether the row should be written at all.
	Changed bool
}

// rewriteProviderEndpoints decodes every provider row, lets one migration
// rewrite it inside a single transaction, and writes back only the rows the
// migration reported as changed. Providers are read first and written after the
// rows are closed because the store uses a single connection.
func (s *Store) rewriteProviderEndpoints(rewrite func(id, baseURL string, raw map[string]storedProviderEndpoint) rewriteResult) error {
	rows, err := s.db.Query(`SELECT id, base_url, endpoints FROM providers ORDER BY id`)
	if err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	type storedProvider struct {
		id        string
		baseURL   string
		endpoints string
	}
	providers := make([]storedProvider, 0)
	for rows.Next() {
		var item storedProvider
		if err := rows.Scan(&item.id, &item.baseURL, &item.endpoints); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate providers: %w", err)
		}
		providers = append(providers, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("migrate providers: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, item := range providers {
		var raw map[string]storedProviderEndpoint
		if err := decodeJSON(item.endpoints, &raw); err != nil {
			return fmt.Errorf("migrate provider %s endpoints: %w", item.id, err)
		}
		result := rewrite(item.id, item.baseURL, raw)
		if !result.Changed {
			continue
		}
		encoded, err := encodeJSON(result.Endpoints)
		if err != nil {
			return fmt.Errorf("migrate provider %s endpoints: %w", item.id, err)
		}
		if result.BaseURL != nil {
			if _, err := tx.Exec(`UPDATE providers SET base_url = ?, endpoints = ? WHERE id = ?`,
				*result.BaseURL, encoded, item.id); err != nil {
				return fmt.Errorf("migrate provider %s: %w", item.id, err)
			}
			continue
		}
		if _, err := tx.Exec(`UPDATE providers SET endpoints = ? WHERE id = ?`, encoded, item.id); err != nil {
			return fmt.Errorf("migrate provider %s: %w", item.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	return nil
}

// migratePerEndpointBaseURLs moves a per-endpoint base_url into the provider
// row and records the endpoint key as its protocol. Newer rows already have the
// shared base URL and are left untouched; the converter migration runs after it.
func (s *Store) migratePerEndpointBaseURLs() error {
	return s.rewriteProviderEndpoints(func(id, storedBaseURL string, raw map[string]storedProviderEndpoint) rewriteResult {
		needsMigration := strings.TrimSpace(storedBaseURL) == ""
		if !needsMigration {
			for _, endpoint := range raw {
				if strings.TrimSpace(endpoint.BaseURL) != "" {
					needsMigration = true
					break
				}
			}
		}
		if !needsMigration {
			return rewriteResult{}
		}

		baseURL := strings.TrimRight(strings.TrimSpace(storedBaseURL), "/")
		endpoints := make(map[string]ProviderEndpointInput, len(raw))
		dropped := make([]string, 0)
		for _, key := range orderedEndpointKeys(raw) {
			endpoint := raw[key]
			endpointBaseURL := strings.TrimRight(strings.TrimSpace(endpoint.BaseURL), "/")
			if endpointBaseURL != "" {
				if baseURL == "" {
					baseURL = endpointBaseURL
				} else if endpointBaseURL != baseURL {
					dropped = append(dropped, key)
					continue
				}
			}
			protocol := strings.TrimSpace(endpoint.Protocol)
			converter := strings.TrimSpace(endpoint.Converter)
			if converter == "" && protocol != "" {
				converter = converterForLegacyProtocol(key, protocol)
			}
			// Rows written before the shared base URL used the endpoint key as
			// the upstream protocol, which means passthrough.
			if converter == "" && endpointBaseURL != "" {
				converter = convert.None
			}
			endpoints[key] = ProviderEndpointInput{Path: endpoint.Path, Converter: converter}
		}
		if len(dropped) > 0 {
			slog.Warn("provider endpoints dropped during migration: endpoints must share one base URL",
				"provider", id, "base_url", baseURL, "endpoints", dropped)
		}
		return rewriteResult{BaseURL: &baseURL, Endpoints: endpoints, Changed: true}
	})
}

// migrateEndpointConverters rewrites the legacy "protocol" endpoint field into
// the current "converter" field. Rows that never used converters keep working
// because an empty converter means passthrough.
func (s *Store) migrateEndpointConverters() error {
	return s.rewriteProviderEndpoints(func(_ string, _ string, raw map[string]storedProviderEndpoint) rewriteResult {
		converted := make(map[string]ProviderEndpointInput, len(raw))
		changed := false
		for _, key := range orderedEndpointKeys(raw) {
			endpoint := raw[key]
			converter := strings.TrimSpace(endpoint.Converter)
			if converter == "" && strings.TrimSpace(endpoint.Protocol) != "" {
				converter = converterForLegacyProtocol(key, endpoint.Protocol)
				changed = true
			}
			converted[key] = ProviderEndpointInput{Path: endpoint.Path, Converter: converter}
		}
		if !changed {
			return rewriteResult{}
		}
		return rewriteResult{Endpoints: converted, Changed: true}
	})
}

// converterForLegacyProtocol maps the old protocol field onto a converter ID.
// The old "same protocol" and "none" values both become passthrough.
func converterForLegacyProtocol(inbound, protocol string) string {
	protocol = strings.TrimSpace(protocol)
	if protocol == "" || protocol == convert.None || protocol == inbound {
		return convert.None
	}
	for _, definition := range convert.Definitions() {
		if string(definition.From) == inbound && string(definition.To) == protocol {
			return definition.ID
		}
	}
	return convert.None
}

// orderedEndpointKeys returns endpoint keys in the canonical wire order, with
// any unknown keys appended in sorted order.
func orderedEndpointKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for _, wire := range WireFormats {
		if _, ok := items[string(wire)]; ok {
			keys = append(keys, string(wire))
		}
	}
	extra := make([]string, 0, len(items))
	for key := range items {
		if !slices.Contains(keys, key) {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return append(keys, extra...)
}

// migrateLegacyBaseURLs rebuilds providers created with a base_urls column
// keyed by vendor protocol. It runs once per database and is written as a flat
// read-copy-write sequence rather than split further.
//
//nolint:gocyclo // one-shot schema rebuild; splitting it would hide the order
func (s *Store) migrateLegacyBaseURLs() error {
	type legacyProvider struct {
		id         string
		name       string
		baseURLs   map[string]string
		apiKey     string
		maxRetries int
		timeout    sql.NullFloat64
		endpoints  map[string]map[string]string
		headers    map[string]string
		proxy      string
	}
	rows, err := s.db.Query(`SELECT id, name, base_urls, api_key, max_retries, timeout, endpoints, headers, proxy
		FROM providers ORDER BY id`)
	if err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	legacy := make([]legacyProvider, 0)
	for rows.Next() {
		var (
			item       legacyProvider
			baseURLs   string
			endpoints  string
			headers    string
			timeout    sql.NullFloat64
			maxRetries int
		)
		if err := rows.Scan(&item.id, &item.name, &baseURLs, &item.apiKey, &maxRetries, &timeout, &endpoints, &headers, &item.proxy); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate providers: %w", err)
		}
		item.maxRetries = maxRetries
		item.timeout = timeout
		if err := decodeJSON(baseURLs, &item.baseURLs); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate provider %s base_urls: %w", item.id, err)
		}
		if err := decodeJSON(endpoints, &item.endpoints); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate provider %s endpoints: %w", item.id, err)
		}
		if err := decodeJSON(headers, &item.headers); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate provider %s headers: %w", item.id, err)
		}
		legacy = append(legacy, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("migrate providers: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}

	// Foreign keys are disabled while the table is rebuilt so the dependent
	// model_providers rows survive the drop and rename.
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	defer func() { _, _ = s.db.Exec(`PRAGMA foreign_keys = ON`) }()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DROP TABLE IF EXISTS providers_migrated`); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	if _, err := tx.Exec(`CREATE TABLE providers_migrated (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL DEFAULT '',
		base_url    TEXT NOT NULL DEFAULT '',
		api_key     TEXT NOT NULL DEFAULT '',
		max_retries INTEGER NOT NULL DEFAULT 2,
		timeout     REAL,
		endpoints   TEXT NOT NULL DEFAULT '{}',
		headers     TEXT NOT NULL DEFAULT '{}',
		proxy       TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	for _, item := range legacy {
		baseURL, endpoints, dropped := legacyEndpointConfig(item.baseURLs, item.endpoints)
		if len(dropped) > 0 {
			slog.Warn("provider endpoints dropped during migration: endpoints must share one base URL",
				"provider", item.id, "name", item.name, "base_url", baseURL, "endpoints", dropped)
		}
		encodedEndpoints, err := encodeJSON(endpoints)
		if err != nil {
			return fmt.Errorf("migrate provider %s endpoints: %w", item.id, err)
		}
		headers, err := encodeJSON(item.headers)
		if err != nil {
			return fmt.Errorf("migrate provider %s headers: %w", item.id, err)
		}
		if _, err := tx.Exec(`INSERT INTO providers_migrated
			(id, name, base_url, api_key, max_retries, timeout, endpoints, headers, proxy)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, item.name, baseURL, item.apiKey, item.maxRetries, nullableFloatValue(item.timeout),
			encodedEndpoints, headers, item.proxy); err != nil {
			return fmt.Errorf("migrate provider %s: %w", item.id, err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE providers`); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE providers_migrated RENAME TO providers`); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate providers: %w", err)
	}
	return nil
}

// legacyEndpointConfig converts the old protocol-keyed base URL and endpoint
// tables into a shared base URL plus the current endpoint records. A protocol's
// base URL applies to every endpoint that protocol used to enable. Endpoints
// that used a different host cannot be represented by one shared base URL, so
// they are dropped; the caller logs the dropped endpoints.
func legacyEndpointConfig(baseURLs map[string]string, endpoints map[string]map[string]string) (string, map[string]ProviderEndpointInput, []string) {
	type legacyEndpoint struct {
		wire      WireFormat
		protocol  string
		configKey string
		path      string
	}
	specs := []legacyEndpoint{
		{wire: WireChatCompletions, protocol: "openai", configKey: "chat_completions", path: "/chat/completions"},
		{wire: WireResponses, protocol: "openai", configKey: "responses", path: "/responses"},
		{wire: WireMessages, protocol: "anthropic", configKey: "messages", path: "/v1/messages"},
	}
	out := make(map[string]ProviderEndpointInput)
	baseURL := ""
	dropped := make([]string, 0)
	for _, spec := range specs {
		protocolBaseURL := strings.TrimRight(strings.TrimSpace(baseURLs[spec.protocol]), "/")
		if protocolBaseURL == "" {
			continue
		}
		if baseURL == "" {
			baseURL = protocolBaseURL
		}
		if protocolBaseURL != baseURL {
			dropped = append(dropped, string(spec.wire))
			continue
		}
		path := spec.path
		if configured := strings.TrimSpace(endpoints[spec.protocol][spec.configKey]); configured != "" {
			path = configured
		}
		out[string(spec.wire)] = ProviderEndpointInput{
			Path:      path,
			Converter: convert.None,
		}
	}
	return baseURL, out, dropped
}

func nullableFloatValue(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}
