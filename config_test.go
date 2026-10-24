package llmproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseConfig = `
[general]
drop_params = true

[server]
host = "127.0.0.1"
port = 4100
log_level = "debug"

[[providers]]
id = "p1"
protocol = "openai"
base_url = "https://example.test/v1/"
key = "k1"
max_retries = 3
endpoints = ["chat/completions", "/chat/completions", "/responses"]
headers = { "x-extra" = "yes" }

[[providers]]
id = "p2"
protocol = "anthropic"
base_url = "https://anthropic.test"
endpoints = ["/messages"]

[[models]]
id = "model/a"
context_length = 1000
max_output_length = 100
modalities = ["text", "image"]
timeout = 12.5
providers = [
  { provider_id = "p1", model = "up-a", weight = 0.4 },
  { provider_id = "p2", model = "up-b", weight = 0.6 },
]

[[models]]
id = "model/b"
providers = [{ provider_id = "p1", model = "up-c" }]

[[users]]
id = "alice"
api_key = "key-a"
allowed_models = ["model/a"]

[[users]]
id = "bob"
api_key = "key-b"
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfigValid(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(writeConfig(t, baseConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !cfg.AuthEnabled() || cfg.DropParams != true {
		t.Fatalf("config flags = %+v", cfg)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 4100 || cfg.LogLevel != "debug" {
		t.Fatalf("server config = %+v", cfg)
	}

	provider := cfg.Providers["p1"]
	if provider.BaseURL != "https://example.test/v1" {
		t.Fatalf("base url = %q", provider.BaseURL)
	}
	if provider.MaxRetries != 3 || provider.Key != "k1" || provider.Protocol != "openai" {
		t.Fatalf("provider = %+v", provider)
	}
	if len(provider.Endpoints) != 2 || provider.Endpoints[0] != "/chat/completions" || provider.Endpoints[1] != "/responses" {
		t.Fatalf("endpoints = %v", provider.Endpoints)
	}
	if provider.Headers["x-extra"] != "yes" {
		t.Fatalf("headers = %v", provider.Headers)
	}

	if got := cfg.Providers["p2"].Endpoints; len(got) != 1 || got[0] != "/messages" {
		t.Fatalf("anthropic endpoints = %v", got)
	}

	model := cfg.Models["model/a"]
	if model.ContextLength == nil || *model.ContextLength != 1000 {
		t.Fatalf("context length = %v", model.ContextLength)
	}
	if model.MaxOutputLength == nil || *model.MaxOutputLength != 100 {
		t.Fatalf("max output = %v", model.MaxOutputLength)
	}
	if model.Timeout == nil || *model.Timeout != 12.5 {
		t.Fatalf("timeout = %v", model.Timeout)
	}
	if len(model.Modalities) != 2 || model.Modalities[1] != "image" {
		t.Fatalf("modalities = %v", model.Modalities)
	}
	if len(model.Providers) != 2 || model.Providers[0].Weight != 0.4 || model.Providers[1].UpstreamModel != "up-b" {
		t.Fatalf("model providers = %+v", model.Providers)
	}

	if got := cfg.Models["model/b"].Modalities; len(got) != 1 || got[0] != "text" {
		t.Fatalf("default modalities = %v", got)
	}
	if got := cfg.Models["model/b"].Providers[0].Weight; got != 1.0 {
		t.Fatalf("default weight = %v", got)
	}

	if !cfg.UsersByKey["key-a"].CanUse("model/a") {
		t.Fatal("alice should use model/a")
	}
	if cfg.UsersByKey["key-a"].CanUse("model/b") {
		t.Fatal("alice must not use model/b")
	}
	if !cfg.UsersByKey["key-b"].CanUse("model/b") {
		t.Fatal("bob should use every model")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(writeConfig(t, `
[[providers]]
id = "p"
base_url = "https://example.test"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AuthEnabled() {
		t.Fatal("auth should be disabled without users")
	}
	if cfg.Host != "0.0.0.0" || cfg.Port != 4000 || cfg.LogLevel != "info" {
		t.Fatalf("defaults = %+v", cfg)
	}
	provider := cfg.Providers["p"]
	if provider.Protocol != "openai" || provider.MaxRetries != 2 {
		t.Fatalf("provider defaults = %+v", provider)
	}
	if len(provider.Endpoints) != 1 || provider.Endpoints[0] != "/chat/completions" {
		t.Fatalf("default endpoints = %v", provider.Endpoints)
	}
}

func TestLoadConfigRejectsInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "provider without id",
			content: "[[providers]]\nbase_url = \"https://x\"\n",
			want:    "providers[0].id",
		},
		{
			name:    "unsupported protocol",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\nprotocol = \"gemini\"\n",
			want:    "protocol",
		},
		{
			name:    "bad proxy",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\nproxy = \"ftp://proxy\"\n",
			want:    "proxy",
		},
		{
			name:    "bool max retries",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\nmax_retries = true\n",
			want:    "max_retries",
		},
		{
			name:    "too many retries",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\nmax_retries = 11\n",
			want:    "max_retries",
		},
		{
			name:    "nan timeout",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\ntimeout = nan\n",
			want:    "timeout",
		},
		{
			name:    "empty endpoints",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\nendpoints = []\n",
			want:    "endpoints",
		},
		{
			name:    "unsupported endpoint",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\nendpoints = [\"/foo\"]\n",
			want:    "unsupported endpoint",
		},
		{
			name:    "duplicate provider",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\n[[providers]]\nid = \"p\"\nbase_url = \"https://y\"\n",
			want:    "duplicate provider",
		},
		{
			name: "unknown provider reference",
			content: `[[models]]
id = "m"
providers = [{ provider_id = "missing", model = "x" }]
`,
			want: "unknown provider",
		},
		{
			name: "no providers",
			content: `[[models]]
id = "m"
providers = []
`,
			want: "non-empty array",
		},
		{
			name: "unknown provider keys",
			content: `[[providers]]
id = "p"
base_url = "https://x"
[[models]]
id = "m"
providers = [{ provider_id = "p", model = "x", extra = 1 }]
`,
			want: "unknown keys",
		},
		{
			name: "negative weight",
			content: `[[providers]]
id = "p"
base_url = "https://x"
[[models]]
id = "m"
providers = [{ provider_id = "p", model = "x", weight = -0.5 }]
`,
			want: "positive number",
		},
		{
			name: "partial weights",
			content: `[[providers]]
id = "p"
base_url = "https://x"
[[providers]]
id = "q"
base_url = "https://y"
[[models]]
id = "m"
providers = [
  { provider_id = "p", model = "x", weight = 0.5 },
  { provider_id = "q", model = "y" },
]
`,
			want: "weight for every provider",
		},
		{
			name: "weights not one",
			content: `[[providers]]
id = "p"
base_url = "https://x"
[[providers]]
id = "q"
base_url = "https://y"
[[models]]
id = "m"
providers = [
  { provider_id = "p", model = "x", weight = 0.4 },
  { provider_id = "q", model = "y", weight = 0.4 },
]
`,
			want: "sum to 1.0",
		},
		{
			name: "duplicate api key",
			content: `[[users]]
id = "a"
api_key = "k"
[[users]]
id = "b"
api_key = "k"
`,
			want: "duplicate api_key",
		},
		{
			name: "unknown allowed model",
			content: `[[users]]
id = "a"
api_key = "k"
allowed_models = ["missing"]
`,
			want: "unknown models",
		},
		{
			name:    "bad port",
			content: "[server]\nport = 70000\n",
			want:    "server.port",
		},
		{
			name:    "zero port",
			content: "[server]\nport = 0\n",
			want:    "server.port",
		},
		{
			name:    "unsupported base url",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"ftp://example.test\"\n",
			want:    "base_url must be an http(s) URL",
		},
		{
			name:    "timeout overflow",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\ntimeout = 1e300\n",
			want:    "timeout is too large",
		},
		{
			name:    "timeout above duration range",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\ntimeout = 9223372037\n",
			want:    "timeout is too large",
		},
		{
			name:    "unknown key",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\nprotcol = \"openai\"\n",
			want:    "unknown configuration keys",
		},
		{
			name:    "duplicate model",
			content: "[[providers]]\nid = \"p\"\nbase_url = \"https://x\"\n[[models]]\nid = \"m\"\nproviders = [{ provider_id = \"p\", model = \"x\" }]\n[[models]]\nid = \"m\"\nproviders = [{ provider_id = \"p\", model = \"x\" }]\n",
			want:    "duplicate model",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, test.content))
			if err == nil {
				t.Fatalf("expected error containing %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not contain %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	t.Parallel()

	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfigAcceptsSocksProxy(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig(writeConfig(t, `
[[providers]]
id = "p"
base_url = "https://x"
proxy = "socks5://user:pass@127.0.0.1:1080"
`))
	if err != nil {
		t.Fatalf("socks5 proxy rejected: %v", err)
	}
}

func TestOutboundFor(t *testing.T) {
	t.Parallel()

	provider := &Provider{Protocol: ProtocolOpenAI, Endpoints: []string{"/chat/completions", "/responses"}}

	cases := []struct {
		name       string
		operation  string
		preferred  WireFormat
		wantPath   string
		wantFormat WireFormat
		ok         bool
	}{
		{"chat prefers chat", "chat", "openai", "/chat/completions", "openai", true},
		{"responses prefers responses", "responses", "responses", "/responses", "responses", true},
		{"fallback to first", "chat", "anthropic", "/chat/completions", "openai", true},
		{"embeddings unsupported", "embeddings", "", "", "", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			endpoint, format, ok := provider.OutboundFor(test.operation, test.preferred)
			if ok != test.ok || endpoint != test.wantPath || format != test.wantFormat {
				t.Fatalf("OutboundFor = %q, %q, %v", endpoint, format, ok)
			}
		})
	}

	anthropic := &Provider{Protocol: ProtocolAnthropic, Endpoints: []string{"/messages"}}
	if endpoint, format, ok := anthropic.OutboundFor("chat", ""); !ok || endpoint != "/messages" || format != "anthropic" {
		t.Fatalf("anthropic outbound = %q, %q, %v", endpoint, format, ok)
	}
}

func TestRepositoryConfigLoads(t *testing.T) {
	t.Parallel()

	path := "config.toml"
	if _, err := os.Stat(path); err != nil {
		path = "config.example.toml"
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("no configuration file present")
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	if len(cfg.Providers) == 0 || len(cfg.Models) == 0 {
		t.Fatalf("unexpected config contents: %+v", cfg)
	}
}

func TestLoadConfigAcceptsLargestSafeTimeout(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(writeConfig(t, `
[[providers]]
id = "p"
base_url = "https://x"
timeout = 9223372036
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if timeout := readTimeoutFor(&Model{}, cfg.Providers["p"]); timeout <= 0 {
		t.Fatalf("converted timeout = %v", timeout)
	}
}

func TestLoadConfigWithRejectsInvalidPortOverride(t *testing.T) {
	t.Parallel()

	for _, port := range []int{-1, 65536} {
		_, err := LoadConfigWith(writeConfig(t, baseConfig), Overrides{Port: port})
		if err == nil || !strings.Contains(err.Error(), "port") {
			t.Fatalf("port %d: error = %v", port, err)
		}
	}
}

func TestLoadConfigWithOverrides(t *testing.T) {
	t.Parallel()

	path := "config.example.toml"
	if _, err := os.Stat(path); err != nil {
		t.Skip("config.example.toml not present")
	}
	cfg, err := LoadConfigWith(path, Overrides{Host: "127.0.0.1", Port: 4100, LogLevel: "debug"})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 4100 || cfg.LogLevel != "debug" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}

	base, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if base.Port != 4000 || base.LogLevel != "info" {
		t.Fatalf("base config changed: %+v", base)
	}
}
