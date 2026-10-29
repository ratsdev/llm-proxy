package core

import (
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

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
	for _, format := range WireFormats {
		if _, ok := items[string(format)]; ok {
			keys = append(keys, string(format))
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
