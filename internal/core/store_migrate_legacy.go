package core

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

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
