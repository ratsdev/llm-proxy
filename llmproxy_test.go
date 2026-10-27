package llmproxy_test

import (
	"path/filepath"
	"testing"

	llmproxy "github.com/ratsdev/llm-proxy"
	"github.com/ratsdev/llm-proxy/internal/gateway"
)

// The root package is the only supported import path, so these tests use it
// exclusively: if the facade stops exposing something the CLI or an embedder
// needs, they stop compiling.

func TestVersionMirrorsGateway(t *testing.T) {
	if llmproxy.Version != gateway.Version {
		t.Fatalf("llmproxy.Version = %q, gateway.Version = %q", llmproxy.Version, gateway.Version)
	}
	if llmproxy.Version == "" {
		t.Fatal("empty version")
	}
}

func TestFacadeOpensStoreAndBuildsApp(t *testing.T) {
	store, err := llmproxy.OpenStore(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	app, err := llmproxy.NewApp(store, nil)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(app.Close)

	if cfg := app.Config(); cfg == nil {
		t.Fatal("app returned no configuration snapshot")
	}

	// The exported aliases must stay usable for embedders.
	var (
		_ *llmproxy.Config
		_ *llmproxy.User
		_ *llmproxy.Model
		_ *llmproxy.Provider
	)
	if llmproxy.DefaultDatabasePath != gateway.DefaultDatabasePath {
		t.Fatalf("DefaultDatabasePath = %q", llmproxy.DefaultDatabasePath)
	}
}
