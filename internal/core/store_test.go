package core

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

// The connection pragmas travel in the DSN, so every connection database/sql
// opens keeps foreign keys, WAL journaling and the busy timeout. A one-off Exec
// would be lost as soon as the pool replaced a broken connection.
func TestOpenStoreAppliesConnectionPragmas(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	var foreignKeys string
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != "1" {
		t.Fatalf("foreign_keys = %q, want 1", foreignKeys)
	}
	var journalMode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var busyTimeout int
	if err := store.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// The lifecycle test drives every entity through the store and the loaded
// configuration snapshot, which is the contract the gateway depends on.
func TestStoreProviderModelUserLifecycle(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))

	provider, err := store.SaveProvider(ProviderInput{
		ID:      "upstream",
		Name:    "Upstream",
		BaseURL: "https://api.example.com/v1",
		Endpoints: map[string]ProviderEndpointInput{
			"chat_completions": {Converter: convert.None},
		},
		APIKey: "sk-upstream",
	})
	if err != nil {
		t.Fatalf("save provider: %v", err)
	}
	if provider.ID != "upstream" {
		t.Fatalf("provider id = %q", provider.ID)
	}

	model, err := store.SaveModel(ModelInput{
		ID:   "public-model",
		Name: "Public Model",
		Providers: []ModelProviderInput{{
			ProviderID:    "upstream",
			UpstreamModel: "upstream-model",
		}},
	})
	if err != nil {
		t.Fatalf("save model: %v", err)
	}
	if model.ID != "public-model" {
		t.Fatalf("model id = %q", model.ID)
	}

	user, err := store.SaveUser(UserInput{
		ID:            "alice",
		Name:          "Alice",
		Password:      "alice-password",
		AllowedModels: []string{"public-model"},
	})
	if err != nil {
		t.Fatalf("save user: %v", err)
	}
	if user.ID != "alice" || user.Password != "" {
		t.Fatalf("saved user = %+v", user)
	}
	key, err := store.CreateAPIKey("alice", "Default")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	if key.Key == "" || key.UserID != "alice" || key.Name != "Default" {
		t.Fatalf("api key = %+v", key)
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Providers["upstream"] == nil {
		t.Fatal("provider missing from config")
	}
	if cfg.Providers["upstream"].APIKey != "sk-upstream" {
		t.Fatalf("provider key = %q", cfg.Providers["upstream"].APIKey)
	}
	loaded := cfg.Models["public-model"]
	if loaded == nil || len(loaded.Providers) != 1 || loaded.Providers[0].UpstreamModel != "upstream-model" {
		t.Fatalf("model = %+v", loaded)
	}
	if cfg.UsersByKey[key.Key] == nil || cfg.UsersByID["alice"] == nil {
		t.Fatalf("user missing from config: %+v", cfg.UsersByKey)
	}
	if cfg.UsersByID["alice"].PasswordHash == "" {
		t.Fatal("user password was not persisted")
	}
	if cfg.UsersByID["alice"].CanUse("other-model") {
		t.Fatal("allowed model list was not loaded")
	}

	if err := store.DeleteProvider("upstream"); err == nil {
		t.Fatal("provider bound to a model must not be deleted")
	}
	if err := store.DeleteModel("public-model"); err != nil {
		t.Fatalf("delete model: %v", err)
	}
	if err := store.DeleteProvider("upstream"); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	if _, err := store.GetProvider("upstream"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted provider lookup = %v", err)
	}

	// Updating a user without a password keeps the existing one.
	updated, err := store.SaveUser(UserInput{ID: "alice", Name: "Alice Updated"})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if updated.KeyCount != 1 {
		t.Fatalf("updated key count = %d", updated.KeyCount)
	}
	if reloaded, err := store.LoadConfig(); err != nil {
		t.Fatalf("reload config: %v", err)
	} else if reloaded.UsersByID["alice"].PasswordHash == "" {
		t.Fatal("updating a user cleared the password")
	}
	if err := store.DeleteUser("alice"); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := store.GetUser("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user lookup = %v", err)
	}
	if cfg, err := store.LoadConfig(); err != nil {
		t.Fatalf("reload after delete: %v", err)
	} else if cfg.UsersByKey[key.Key] != nil {
		t.Fatal("deleted user's API key survived")
	}
}
