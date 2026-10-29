package core

import (
	"fmt"
	"time"
)

// migrate creates the current schema and runs the schema migrations in the
// order later migrations depend on.
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

// tableColumns reports which columns a table currently has.
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
