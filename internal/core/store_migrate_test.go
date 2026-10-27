package core

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

// Each test seeds a database written by an older release and asserts that
// opening it migrates the schema without losing operator configuration.

func TestStoreMigratesLegacyProviderEndpoints(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	statements := []string{
		`CREATE TABLE providers (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL DEFAULT '',
			base_urls   TEXT NOT NULL,
			api_key     TEXT NOT NULL DEFAULT '',
			max_retries INTEGER NOT NULL DEFAULT 2,
			timeout     REAL,
			endpoints   TEXT NOT NULL DEFAULT '{}',
			headers     TEXT NOT NULL DEFAULT '{}',
			proxy       TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO providers (id, name, base_urls, api_key, max_retries, endpoints, headers, proxy)
			VALUES ('legacy', 'Legacy', '{"openai":"https://api.openai.com/v1","anthropic":"https://api.anthropic.com"}',
				'sk-legacy', 3,
				'{"openai":{"chat_completions":"/v1/chat/completions"},"anthropic":{"messages":"/v1/messages"}}',
				'{}', '')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare legacy database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store := openTestStore(t, path)
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	provider := cfg.Providers["legacy"]
	if provider == nil {
		t.Fatal("legacy provider was not loaded")
	}
	if provider.MaxRetries != 3 || provider.APIKey != "sk-legacy" {
		t.Fatalf("legacy provider settings = %+v", provider)
	}
	if provider.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("legacy base URL = %q", provider.BaseURL)
	}
	chat, ok := provider.Endpoints[WireChatCompletions]
	if !ok || chat.Converter != convert.None || chat.Path != "/v1/chat/completions" {
		t.Fatalf("chat_completions = %+v, ok=%v", chat, ok)
	}
	responses, ok := provider.Endpoints[WireResponses]
	if !ok || responses.Converter != convert.None || responses.Path != "/responses" {
		t.Fatalf("responses = %+v, ok=%v", responses, ok)
	}
	if _, ok := provider.Endpoints[WireMessages]; ok {
		t.Fatal("messages endpoint on a different host must be dropped by migration")
	}

	var legacyColumns int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('providers') WHERE name = 'base_urls'`).Scan(&legacyColumns); err != nil {
		t.Fatalf("inspect migrated table: %v", err)
	}
	if legacyColumns != 0 {
		t.Fatal("base_urls column survived the migration")
	}
}

func TestStoreMigratesModelCapabilitiesAndProviderCatalog(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	statements := []string{
		`CREATE TABLE providers (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL DEFAULT '',
			base_url    TEXT NOT NULL DEFAULT '',
			api_key     TEXT NOT NULL DEFAULT '',
			max_retries INTEGER NOT NULL DEFAULT 2,
			timeout     REAL,
			endpoints   TEXT NOT NULL DEFAULT '{}',
			headers     TEXT NOT NULL DEFAULT '{}',
			proxy       TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE models (
			id                TEXT PRIMARY KEY,
			name              TEXT NOT NULL DEFAULT '',
			context_length    INTEGER,
			max_output_length INTEGER,
			modalities        TEXT NOT NULL DEFAULT '["text"]',
			output_modalities TEXT NOT NULL DEFAULT '["text"]',
			timeout           REAL
		)`,
		`CREATE TABLE model_providers (
			model_id       TEXT NOT NULL,
			provider_id    TEXT NOT NULL,
			upstream_model TEXT NOT NULL,
			weight         REAL NOT NULL DEFAULT 1,
			max_retry_after REAL,
			position       INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (model_id, provider_id, upstream_model)
		)`,
		`INSERT INTO providers (id, name, base_url, endpoints)
			VALUES ('legacy', 'Legacy', 'https://api.example.com/v1', '{"chat_completions":{"converter":"none"}}')`,
		`INSERT INTO models (id, name) VALUES ('legacy-model', 'Legacy Model')`,
		`INSERT INTO model_providers (model_id, provider_id, upstream_model, weight)
			VALUES ('legacy-model', 'legacy', 'legacy-upstream', 0.5)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare legacy database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store := openTestStore(t, path)
	provider, err := store.GetProvider("legacy")
	if err != nil {
		t.Fatalf("get migrated provider: %v", err)
	}
	if len(provider.UpstreamModels) != 0 {
		t.Fatalf("migrated upstream models = %v", provider.UpstreamModels)
	}
	model, err := store.GetModel("legacy-model")
	if err != nil {
		t.Fatalf("get migrated model: %v", err)
	}
	if model.Reasoning || model.Tools {
		t.Fatalf("migrated model capabilities = %+v", model)
	}
	if model.Routing != RoutingPriority {
		t.Fatalf("migrated model routing = %q", model.Routing)
	}
	if len(model.Providers) != 1 || model.Providers[0].UpstreamModel != "legacy-upstream" {
		t.Fatalf("migrated bindings = %+v", model.Providers)
	}
	columns, err := store.tableColumns("model_providers")
	if err != nil {
		t.Fatalf("inspect migrated bindings: %v", err)
	}
	if columns["weight"] {
		t.Fatal("weight column survived the migration")
	}
}

func TestStoreMigratesLegacyProtocolToConverter(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "legacy-converter.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE providers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		base_url TEXT NOT NULL DEFAULT '',
		api_key TEXT NOT NULL DEFAULT '',
		max_retries INTEGER NOT NULL DEFAULT 2,
		timeout REAL,
		endpoints TEXT NOT NULL DEFAULT '{}',
		headers TEXT NOT NULL DEFAULT '{}',
		proxy TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create providers: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO providers (id, base_url, endpoints) VALUES (?, ?, ?)`,
		"legacy", "https://api.anthropic.com",
		`{"chat_completions":{"path":"/v1/messages","protocol":"messages"}}`); err != nil {
		t.Fatalf("insert legacy provider: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	endpoint := cfg.Providers["legacy"].Endpoints[WireChatCompletions]
	if endpoint.Converter != convert.OpenAIChatToClaudeMessages {
		t.Fatalf("migrated converter = %q", endpoint.Converter)
	}
}

func TestStoreMigratesPerEndpointBaseURLs(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "intermediate.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open intermediate database: %v", err)
	}
	statements := []string{
		`CREATE TABLE providers (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL DEFAULT '',
			api_key     TEXT NOT NULL DEFAULT '',
			max_retries INTEGER NOT NULL DEFAULT 2,
			timeout     REAL,
			endpoints   TEXT NOT NULL DEFAULT '{}',
			headers     TEXT NOT NULL DEFAULT '{}',
			proxy       TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO providers (id, name, api_key, max_retries, endpoints, headers, proxy)
			VALUES ('mid', 'Intermediate', 'sk-mid', 2,
				'{"chat_completions":{"base_url":"https://api.example.com/v1","path":"/chat/completions"}}',
				'{}', '')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare intermediate database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close intermediate database: %v", err)
	}

	store := openTestStore(t, path)
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	provider := cfg.Providers["mid"]
	if provider == nil {
		t.Fatal("provider missing after migration")
	}
	if provider.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("migrated base URL = %q", provider.BaseURL)
	}
	endpoint := provider.Endpoints[WireChatCompletions]
	if endpoint.Path != "/chat/completions" || endpoint.ConverterID() != convert.None {
		t.Fatalf("migrated endpoint = %+v", endpoint)
	}
}

func TestStoreMigratesLegacyUserAPIKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "legacy-users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	statements := []string{
		`CREATE TABLE users (
			id             TEXT PRIMARY KEY,
			name           TEXT NOT NULL DEFAULT '',
			api_key        TEXT,
			allowed_models TEXT NOT NULL DEFAULT '[]',
			is_admin       INTEGER NOT NULL DEFAULT 0,
			password_hash  TEXT NOT NULL DEFAULT '',
			created_at     TEXT NOT NULL,
			updated_at     TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX users_api_key_unique
			ON users(api_key) WHERE api_key IS NOT NULL AND api_key <> ''`,
		`INSERT INTO users (id, name, api_key, allowed_models, is_admin, password_hash, created_at, updated_at)
			VALUES ('legacy', 'Legacy', 'sk-legacy', '[]', 0, '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare legacy database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store := openTestStore(t, path)
	keys, err := store.ListAPIKeys("legacy")
	if err != nil {
		t.Fatalf("list migrated keys: %v", err)
	}
	if len(keys) != 1 || keys[0].Key != "sk-legacy" || keys[0].Name != "Default" {
		t.Fatalf("migrated keys = %+v", keys)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.UsersByKey["sk-legacy"] == nil || cfg.UsersByKey["sk-legacy"].ID != "legacy" {
		t.Fatalf("legacy key missing from config: %+v", cfg.UsersByKey)
	}
	columns, err := store.tableColumns("users")
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	if columns["api_key"] {
		t.Fatal("legacy api_key column survived the migration")
	}
}
