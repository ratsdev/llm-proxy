package ui

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

// The console renders a fallback converter list when the API is unavailable, so
// the bundle and the Go registry must agree; otherwise an operator edits a
// provider with a converter the server would reject (or misses a new one).
func TestEmbeddedConsoleListsEveryConverter(t *testing.T) {
	t.Parallel()

	bundle, err := fs.ReadFile(uiAssets, "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	for _, definition := range convert.Definitions() {
		if !strings.Contains(string(bundle), definition.ID) {
			t.Errorf("console bundle does not mention converter %q", definition.ID)
		}
	}
	for _, format := range convert.Formats() {
		if !strings.Contains(string(bundle), string(format)) {
			t.Errorf("console bundle does not mention wire format %q", format)
		}
	}
}

func TestEmbeddedConsoleServesIndexAndAssets(t *testing.T) {
	t.Parallel()

	if !strings.Contains(string(uiIndexHTML), "/assets/app.js") {
		t.Fatal("index.html does not load the console bundle")
	}
	for _, name := range []string{"app.js", "format.js", "styles.css"} {
		if _, ok := uiAssetETags[name]; !ok {
			t.Errorf("asset %q has no ETag entry", name)
		}
	}
}
