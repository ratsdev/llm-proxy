package ui

import "testing"

// A model can bind the same provider twice with different upstream names. The
// provider view must still list that model once.
func TestProviderModelsDeduplicatesRepeatedBindings(t *testing.T) {
	t.Parallel()

	model := &Model{ID: "alias", Providers: []ModelProvider{
		{ProviderID: "p", UpstreamModel: "up-a"},
		{ProviderID: "p", UpstreamModel: "up-b"},
	}}
	cfg := &Config{
		Providers: map[string]*Provider{"p": {ID: "p"}},
		Models:    map[string]*Model{"alias": model},
	}
	grouped := providerModels(cfg)
	if ids := grouped["p"]; len(ids) != 1 || ids[0].ID != "alias" {
		t.Fatalf("provider models = %v", ids)
	}
}
