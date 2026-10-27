package core

import (
	"path/filepath"
	"strings"
	"testing"
)

// LoadConfig is the bridge between stored rows and the request path, so the
// rules it enforces are covered here rather than through a full gateway test.

func TestLoadConfigRejectsUnknownProviderReference(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	// Bypass SaveModel's validation to simulate a database edited outside the
	// gateway (or a partial restore). Referential integrity has to be switched
	// off for the seed; the store uses a single connection, so the PRAGMA
	// applies to the statements below.
	if _, err := store.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	t.Cleanup(func() { _, _ = store.db.Exec(`PRAGMA foreign_keys = ON`) })
	if _, err := store.db.Exec(`INSERT INTO models (id, name) VALUES ('broken', 'Broken')`); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_providers (model_id, provider_id, upstream_model)
		VALUES ('broken', 'ghost', 'up')`); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	if _, err := store.LoadConfig(); err == nil {
		t.Fatal("model bound to an unknown provider was loaded")
	} else if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadConfigSkipsEmptyAPIKey(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	if _, err := store.SaveUser(UserInput{ID: "alice", Password: "alice-password"}); err != nil {
		t.Fatalf("save user: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO api_keys (id, user_id, name, key, created_at)
		VALUES ('key-empty', 'alice', 'Empty', '', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed empty key: %v", err)
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.UsersByKey) != 0 {
		t.Fatalf("empty key authenticated: %+v", cfg.UsersByKey)
	}
	// An empty key table disables authentication entirely, which would be a
	// serious regression if an empty row could produce it.
	if cfg.AuthEnabled() {
		t.Fatal("auth reported as enabled without a usable key")
	}
}

func TestLoadConfigReadsDropParamsSetting(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	if _, err := store.db.Exec(`INSERT INTO settings (key, value) VALUES ('drop_params', 'TRUE')`); err != nil {
		t.Fatalf("seed setting: %v", err)
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.DropParams {
		t.Fatal("drop_params setting was not applied")
	}
}

func TestLoadConfigDefaultsWithoutSettings(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DropParams {
		t.Fatal("drop_params defaulted to enabled")
	}
	if len(cfg.Providers) != 0 || len(cfg.Models) != 0 || len(cfg.UsersByKey) != 0 {
		t.Fatalf("empty database produced a non-empty snapshot")
	}
}
