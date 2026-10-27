package core

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

func TestStoreRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))

	if _, err := store.SaveProvider(ProviderInput{ID: "bad id"}); err == nil {
		t.Fatal("invalid provider id was accepted")
	}
	if _, err := store.SaveProvider(ProviderInput{
		ID:      " padded ",
		BaseURL: "https://api.example.com/v1",
		Endpoints: map[string]ProviderEndpointInput{
			"chat_completions": {Converter: convert.None},
		},
	}); err == nil {
		t.Fatal("provider id with leading/trailing whitespace was silently trimmed")
	}
	if _, err := store.SaveModel(ModelInput{ID: "m"}); err == nil {
		t.Fatal("model without providers was accepted")
	}
	if _, err := store.SaveModel(ModelInput{
		ID: "m",
		Providers: []ModelProviderInput{{
			ProviderID:    "missing",
			UpstreamModel: "up",
		}},
	}); err == nil {
		t.Fatal("model bound to an unknown provider was accepted")
	}
	if _, err := store.SaveModel(ModelInput{
		ID:      "m",
		Routing: "fastest",
		Providers: []ModelProviderInput{{
			ProviderID:    "missing",
			UpstreamModel: "up",
		}},
	}); err == nil {
		t.Fatal("invalid routing strategy was accepted")
	}
	if _, err := store.SaveUser(UserInput{ID: "short", Password: "short"}); err == nil {
		t.Fatal("short password was accepted")
	}
	if _, err := store.SaveUser(UserInput{ID: "nopass"}); err == nil {
		t.Fatal("new user without a password was accepted")
	}
}

func TestStoreRejectsInvalidProviderBaseURLAndConverter(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	cases := []ProviderInput{
		{
			ID:        "missing-base",
			Endpoints: map[string]ProviderEndpointInput{"chat_completions": {}},
		},
		{
			ID:        "bad-base",
			BaseURL:   "ftp://api.example.com",
			Endpoints: map[string]ProviderEndpointInput{"chat_completions": {}},
		},
		{
			ID:        "bad-converter",
			BaseURL:   "https://api.example.com",
			Endpoints: map[string]ProviderEndpointInput{"chat_completions": {Converter: "grpc"}},
		},
		{
			ID:        "no-converter",
			BaseURL:   "https://api.example.com",
			Endpoints: map[string]ProviderEndpointInput{"chat_completions": {Converter: "claude_messages_to_openai_chat"}},
		},
		{
			ID:        "bad-path",
			BaseURL:   "https://api.example.com",
			Endpoints: map[string]ProviderEndpointInput{"chat_completions": {Path: "chat"}},
		},
	}
	for _, input := range cases {
		if _, err := store.SaveProvider(input); err == nil {
			t.Fatalf("provider %q was accepted", input.ID)
		}
	}
}

// A model that binds the same provider twice to the same upstream model must be
// rejected by validation, not by the model_providers primary key.
func TestStoreRejectsDuplicateModelBinding(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	if _, err := store.SaveProvider(ProviderInput{
		ID:        "upstream",
		BaseURL:   "https://api.example.com/v1",
		Endpoints: map[string]ProviderEndpointInput{"chat_completions": {Converter: convert.None}},
	}); err != nil {
		t.Fatalf("save provider: %v", err)
	}

	_, err := store.SaveModel(ModelInput{
		ID: "alias",
		Providers: []ModelProviderInput{
			{ProviderID: "upstream", UpstreamModel: "up-model"},
			{ProviderID: "upstream", UpstreamModel: "up-model"},
		},
	})
	if err == nil {
		t.Fatal("duplicate binding was accepted")
	}
	if !strings.Contains(err.Error(), "duplicates providers[0]") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigIDRejectsUnsafeIDs(t *testing.T) {
	t.Parallel()

	valid := []string{"upstream", "team/a", "openai.prod_1"}
	for _, id := range valid {
		if _, err := configID("provider", id); err != nil {
			t.Fatalf("configID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "  ", " padded", "line\nbreak", "tab\tid"} {
		if _, err := configID("provider", id); err == nil {
			t.Fatalf("configID(%q) was accepted", id)
		}
	}
}
