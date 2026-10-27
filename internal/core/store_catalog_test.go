package core

import (
	"path/filepath"
	"testing"

	"github.com/ratsdev/llm-proxy/internal/convert"
)

func TestStoreProviderModelCapabilitiesAndCatalog(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	provider, err := store.SaveProvider(ProviderInput{
		ID:             "upstream",
		BaseURL:        "https://api.example.com/v1",
		UpstreamModels: []string{"gpt-4o", "gpt-4o-mini", "gpt-4o", " "},
		Endpoints: map[string]ProviderEndpointInput{
			"chat_completions": {Converter: convert.None},
		},
	})
	if err != nil {
		t.Fatalf("save provider: %v", err)
	}
	if len(provider.UpstreamModels) != 2 || provider.UpstreamModels[0] != "gpt-4o" {
		t.Fatalf("upstream models = %v", provider.UpstreamModels)
	}

	if _, err := store.SaveModel(ModelInput{
		ID: "alias",
		Providers: []ModelProviderInput{{
			ProviderID:    "upstream",
			UpstreamModel: "not-in-catalog",
		}},
	}); err == nil {
		t.Fatal("model bound to a model outside the provider catalog was accepted")
	}

	if _, err := store.SaveModel(ModelInput{
		ID:        "alias",
		Name:      "Alias",
		Routing:   RoutingRoundRobin,
		Reasoning: true,
		Tools:     true,
		Providers: []ModelProviderInput{{
			ProviderID:    "upstream",
			UpstreamModel: "gpt-4o",
		}},
	}); err != nil {
		t.Fatalf("save model: %v", err)
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	loaded := cfg.Models["alias"]
	if loaded == nil || !loaded.Reasoning || !loaded.Tools {
		t.Fatalf("model capabilities = %+v", loaded)
	}
	if loaded.Routing != RoutingRoundRobin {
		t.Fatalf("model routing = %q", loaded.Routing)
	}
	if got := cfg.Providers["upstream"].UpstreamModels; len(got) != 2 || got[1] != "gpt-4o-mini" {
		t.Fatalf("loaded upstream models = %v", got)
	}
	if got, err := store.GetModel("alias"); err != nil || !got.Reasoning || !got.Tools {
		t.Fatalf("stored model capabilities = %+v, err=%v", got, err)
	}

	if _, err := store.SaveProvider(ProviderInput{
		ID:             "upstream",
		BaseURL:        "https://api.example.com/v1",
		UpstreamModels: []string{"gpt-4o-mini"},
		Endpoints: map[string]ProviderEndpointInput{
			"chat_completions": {Converter: convert.None},
		},
	}); err == nil {
		t.Fatal("provider catalog edit dropped an upstream model that is still bound")
	}
}

func TestStoreSaveProviderKeepsBlankAPIKeyOnUpdate(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	input := ProviderInput{
		ID:      "p",
		BaseURL: "https://api.example.com/v1",
		APIKey:  "sk-original",
		Endpoints: map[string]ProviderEndpointInput{
			"chat_completions": {Converter: convert.None},
		},
	}
	if _, err := store.SaveProvider(input); err != nil {
		t.Fatalf("save provider: %v", err)
	}

	input.APIKey = ""
	saved, err := store.SaveProvider(input)
	if err != nil {
		t.Fatalf("update provider: %v", err)
	}
	if saved.APIKey != "sk-original" {
		t.Fatalf("blank update cleared the key: %q", saved.APIKey)
	}
	reloaded, err := store.GetProvider("p")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if reloaded.APIKey != "sk-original" {
		t.Fatalf("stored key = %q", reloaded.APIKey)
	}

	input.APIKey = "sk-replaced"
	if _, err := store.SaveProvider(input); err != nil {
		t.Fatalf("replace key: %v", err)
	}
	if reloaded, err = store.GetProvider("p"); err != nil || reloaded.APIKey != "sk-replaced" {
		t.Fatalf("replaced key = %+v, %v", reloaded, err)
	}
}

func TestStoreProviderSharesBaseURL(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	saved, err := store.SaveProvider(ProviderInput{
		ID:      "p1",
		BaseURL: "https://api.example.com/v1/",
		Endpoints: map[string]ProviderEndpointInput{
			"chat_completions": {Path: "/v1/chat/completions", Converter: convert.None},
			"responses":        {Converter: convert.None},
		},
	})
	if err != nil {
		t.Fatalf("save provider: %v", err)
	}
	if saved.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("saved base URL = %q", saved.BaseURL)
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	provider := cfg.Providers["p1"]
	if provider == nil {
		t.Fatal("provider missing from config")
	}
	if provider.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("base URL = %q", provider.BaseURL)
	}
	chat := provider.Endpoints[WireChatCompletions]
	if chat.Path != "/v1/chat/completions" {
		t.Fatalf("chat path = %q", chat.Path)
	}
	if chat.ConverterID() != convert.None {
		t.Fatalf("chat converter = %q", chat.ConverterID())
	}
	if got := provider.URLFor(chat); got != "https://api.example.com/v1/v1/chat/completions" {
		t.Fatalf("URLFor = %q", got)
	}
	responses := provider.Endpoints[WireResponses]
	if responses.ConverterID() != convert.None || responses.Path != "/responses" {
		t.Fatalf("responses endpoint = %+v", responses)
	}
}

func TestStorePersistsCrossProtocolConverter(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	if _, err := store.SaveProvider(ProviderInput{
		ID:      "claude",
		BaseURL: "https://api.anthropic.com",
		Endpoints: map[string]ProviderEndpointInput{
			"chat_completions": {
				Path:      "/v1/messages",
				Converter: convert.OpenAIChatToClaudeMessages,
			},
		},
	}); err != nil {
		t.Fatalf("save provider: %v", err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	endpoint := cfg.Providers["claude"].Endpoints[WireChatCompletions]
	if endpoint.Converter != convert.OpenAIChatToClaudeMessages {
		t.Fatalf("converter = %q", endpoint.Converter)
	}
	wire, ok := endpoint.UpstreamFormat(WireChatCompletions)
	if !ok || wire != WireMessages {
		t.Fatalf("upstream format = %q, ok=%v", wire, ok)
	}
}

// A blank key on an update keeps whatever the provider already had, which is
// what the console's edit form promises.
func TestStoreSaveProviderKeepsStoredKey(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	base := ProviderInput{
		ID:        "p",
		BaseURL:   "https://api.example.com/v1",
		APIKey:    "sk-original",
		Endpoints: map[string]ProviderEndpointInput{"chat_completions": {}},
	}
	if _, err := store.SaveProvider(base); err != nil {
		t.Fatalf("SaveProvider() error = %v", err)
	}

	base.APIKey = ""
	if _, err := store.SaveProvider(base); err != nil {
		t.Fatalf("SaveProvider(blank key) error = %v", err)
	}
	if stored, err := store.GetProvider("p"); err != nil || stored.APIKey != "sk-original" {
		t.Fatalf("GetProvider() = %q, %v; want the stored key", stored.APIKey, err)
	}
}

func TestStoreSaveModelReturnsNormalisedInput(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "proxy.db"))
	if _, err := store.SaveProvider(ProviderInput{
		ID:             "p",
		BaseURL:        "https://api.example.com/v1",
		Endpoints:      map[string]ProviderEndpointInput{"chat_completions": {}},
		UpstreamModels: []string{"gpt-4o"},
	}); err != nil {
		t.Fatalf("SaveProvider() error = %v", err)
	}

	// routing and modalities are omitted on purpose: the returned input must show
	// what the live configuration will use, not the raw request.
	saved, err := store.SaveModel(ModelInput{
		ID:        "alias",
		Providers: []ModelProviderInput{{ProviderID: "p", UpstreamModel: "gpt-4o"}},
	})
	if err != nil {
		t.Fatalf("SaveModel() error = %v", err)
	}
	if saved.Routing != RoutingPriority {
		t.Errorf("SaveModel().Routing = %q, want %q", saved.Routing, RoutingPriority)
	}
	if len(saved.Modalities) != 1 || saved.Modalities[0] != "text" {
		t.Errorf("SaveModel().Modalities = %v, want [text]", saved.Modalities)
	}
	if len(saved.Providers) != 1 || saved.Providers[0].UpstreamModel != "gpt-4o" {
		t.Errorf("SaveModel().Providers = %+v, want the saved binding", saved.Providers)
	}
}
