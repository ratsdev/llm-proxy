package llmproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseConfig = `
general:
  drop_params: true

server:
  host: "127.0.0.1"
  port: 4100
  log_level: "debug"

providers:
  p1:
    name: "Provider One"
    base_url:
      openai: "https://example.test/v1/"
    api_key: " k1 "
    max_retries: 3
    endpoints:
      openai:
        chat_completions: "/chat/completions"
        responses: "/responses"
    headers:
      x-extra: "yes"

  p2:
    name: "Provider Two"
    base_url:
      anthropic: "https://anthropic.test"
    endpoints:
      anthropic:
        messages: "/messages"

models:
  model/a:
    name: "Model A"
    context_length: 1000
    max_output_length: 100
    modalities:
      input: ["text", "image"]
      output: ["text"]
    timeout: 12.5
    providers:
      - provider_id: "p1"
        model_id: "up-a"
        weight: 0.4
        max_retry_after: 2.5
      - provider_id: "p2"
        model_id: "up-b"
        weight: 0.6

  model/b:
    providers:
      - provider_id: "p1"
        model_id: "up-c"

users:
  alice:
    name: "Alice"
    api_key: "key-a"
    allowed_models: ["model/a"]

  bob:
    api_key: "key-b"
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
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
	if provider.Name != "Provider One" {
		t.Fatalf("provider name = %q", provider.Name)
	}
	if provider.BaseURLs[ProtocolOpenAI] != "https://example.test/v1" {
		t.Fatalf("base url = %q", provider.BaseURLs[ProtocolOpenAI])
	}
	if provider.MaxRetries != 3 || provider.Key != "k1" {
		t.Fatalf("provider = %+v", provider)
	}
	endpoints := provider.Endpoints[ProtocolOpenAI]
	if len(endpoints) != 2 ||
		endpoints[0].Name != "chat_completions" || endpoints[0].Path != "/chat/completions" ||
		endpoints[1].Name != "responses" || endpoints[1].Path != "/responses" {
		t.Fatalf("endpoints = %+v", endpoints)
	}
	if provider.Headers["x-extra"] != "yes" {
		t.Fatalf("headers = %v", provider.Headers)
	}

	anthropicEndpoints := cfg.Providers["p2"].Endpoints[ProtocolAnthropic]
	if len(anthropicEndpoints) != 1 || anthropicEndpoints[0].Path != "/messages" {
		t.Fatalf("anthropic endpoints = %+v", anthropicEndpoints)
	}

	model := cfg.Models["model/a"]
	if model.Name != "Model A" {
		t.Fatalf("model name = %q", model.Name)
	}
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
	if len(model.OutputModalities) != 1 || model.OutputModalities[0] != "text" {
		t.Fatalf("output modalities = %v", model.OutputModalities)
	}
	if len(model.Providers) != 2 || model.Providers[0].Weight != 0.4 || model.Providers[1].UpstreamModel != "up-b" {
		t.Fatalf("model providers = %+v", model.Providers)
	}
	if model.Providers[0].MaxRetryAfter == nil || *model.Providers[0].MaxRetryAfter != 2.5 {
		t.Fatalf("max_retry_after = %v", model.Providers[0].MaxRetryAfter)
	}
	if model.Providers[1].MaxRetryAfter != nil {
		t.Fatalf("unset max_retry_after = %v", model.Providers[1].MaxRetryAfter)
	}

	defaultModel := cfg.Models["model/b"]
	if len(defaultModel.Modalities) != 1 || defaultModel.Modalities[0] != "text" {
		t.Fatalf("default modalities = %v", defaultModel.Modalities)
	}
	if len(defaultModel.OutputModalities) != 1 || defaultModel.OutputModalities[0] != "text" {
		t.Fatalf("default output modalities = %v", defaultModel.OutputModalities)
	}
	if got := defaultModel.Providers[0].Weight; got != 1.0 {
		t.Fatalf("default weight = %v", got)
	}

	if cfg.UsersByKey["key-a"].Name != "Alice" {
		t.Fatalf("user name = %q", cfg.UsersByKey["key-a"].Name)
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
providers:
  p:
    base_url:
      openai: "https://example.test"
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
	if provider.MaxRetries != 2 {
		t.Fatalf("provider defaults = %+v", provider)
	}
	defaults := provider.Endpoints[ProtocolOpenAI]
	wantPaths := []string{"/chat/completions", "/responses"}
	if len(defaults) != len(wantPaths) {
		t.Fatalf("default endpoints = %+v", provider.Endpoints)
	}
	for i, want := range wantPaths {
		if defaults[i].Path != want {
			t.Fatalf("default endpoint %d = %q, want %q", i, defaults[i].Path, want)
		}
	}
}

func TestLoadConfigDefaultEndpointsForBothProtocols(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(writeConfig(t, `
providers:
  dual:
    base_url:
      openai: "https://openai.test/v1"
      anthropic: "https://anthropic.test"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	provider := cfg.Providers["dual"]
	if got := len(provider.Endpoints[ProtocolOpenAI]); got != 2 {
		t.Fatalf("default openai endpoints = %+v", provider.Endpoints[ProtocolOpenAI])
	}
	anthropic := provider.Endpoints[ProtocolAnthropic]
	if len(anthropic) != 1 || anthropic[0].Path != "/v1/messages" {
		t.Fatalf("default anthropic endpoints = %+v", anthropic)
	}

	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "dual", UpstreamModel: "up", Weight: 1}}}
	router := &Router{cfg: cfg}
	candidates, err := router.candidates(model, wireAnthropic, false)
	if err != nil || len(candidates) != 1 || candidates[0].endpoint != "/v1/messages" {
		t.Fatalf("anthropic candidate = %+v, err = %v", candidates, err)
	}
}

func TestLoadConfigEndpointAllowList(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(writeConfig(t, `
providers:
  dual:
    base_url:
      openai: "https://openai.test/v1"
      anthropic: "https://anthropic.test"
    endpoints:
      openai:
        chat_completions:
        responses: "/custom/responses"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	provider := cfg.Providers["dual"]
	openai := provider.Endpoints[ProtocolOpenAI]
	if len(openai) != 2 ||
		openai[0].Name != "chat_completions" || openai[0].Path != "/chat/completions" ||
		openai[1].Name != "responses" || openai[1].Path != "/custom/responses" {
		t.Fatalf("allow-listed openai endpoints = %+v", openai)
	}
	if got := provider.Endpoints[ProtocolAnthropic]; len(got) != 1 || got[0].Path != "/v1/messages" {
		t.Fatalf("unlisted protocols should keep their default endpoints, got %+v", got)
	}
}

func TestLoadConfigEmptyEndpointPathUsesDefault(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(writeConfig(t, `
providers:
  p:
    base_url:
      openai: "https://openai.test/v1"
    endpoints:
      openai:
        chat_completions: ""
        responses: "/v1/responses"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	endpoints := cfg.Providers["p"].Endpoints[ProtocolOpenAI]
	if len(endpoints) != 2 ||
		endpoints[0].Name != "chat_completions" || endpoints[0].Path != "/chat/completions" ||
		endpoints[1].Name != "responses" || endpoints[1].Path != "/v1/responses" {
		t.Fatalf("endpoints = %+v", endpoints)
	}
}

func TestLoadConfigDualProtocolProvider(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(writeConfig(t, `
providers:
  dual:
    base_url:
      openai: "https://openai.test/v1"
      anthropic: "https://anthropic.test"
    endpoints:
      openai:
        chat_completions: "/chat/completions"
        responses: "/responses"
      anthropic:
        messages: "/v1/messages"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	provider := cfg.Providers["dual"]
	if len(provider.Protocols()) != 2 {
		t.Fatalf("protocols = %v", provider.Protocols())
	}
	if got := provider.Endpoints[ProtocolAnthropic]; len(got) != 1 || got[0].Path != "/v1/messages" {
		t.Fatalf("anthropic endpoints = %+v", got)
	}
	if got := provider.URLFor(ProtocolAnthropic, "/v1/messages"); got != "https://anthropic.test/v1/messages" {
		t.Fatalf("anthropic URL = %q", got)
	}

	model := &Model{ID: "m", Providers: []ModelProvider{{ProviderID: "dual", UpstreamModel: "up", Weight: 1}}}
	router := &Router{cfg: cfg}
	openAICandidate, err := router.candidates(model, wireOpenAI, false)
	if err != nil || len(openAICandidate) != 1 || openAICandidate[0].protocol != ProtocolOpenAI ||
		openAICandidate[0].endpoint != "/chat/completions" {
		t.Fatalf("openai candidate = %+v, err = %v", openAICandidate, err)
	}
	anthropicCandidate, err := router.candidates(model, wireAnthropic, false)
	if err != nil || len(anthropicCandidate) != 1 || anthropicCandidate[0].protocol != ProtocolAnthropic ||
		anthropicCandidate[0].endpoint != "/v1/messages" {
		t.Fatalf("anthropic candidate = %+v, err = %v", anthropicCandidate, err)
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
			name: "provider without id",
			content: `
providers:
  "":
    base_url:
      openai: "https://x"
`,
			want: "provider ids must be non-empty",
		},
		{
			name: "provider id whitespace",
			content: `
providers:
  " p ":
    base_url:
      openai: "https://x"
`,
			want: "leading or trailing whitespace",
		},
		{
			name: "invalid header name",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    headers:
      "x-bad name": "value"
`,
			want: "not a valid header name",
		},
		{
			name: "header value control character",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    headers:
      x-bad: "a\nb"
`,
			want: "control characters",
		},
		{
			name: "model id whitespace",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
models:
  " m ":
    providers:
      - provider_id: "p"
        model_id: "x"
`,
			want: "leading or trailing whitespace",
		},
		{
			name: "user id whitespace",
			content: `
users:
  " alice ":
    api_key: "k"
`,
			want: "leading or trailing whitespace",
		},
		{
			name: "unsupported protocol",
			content: `
providers:
  p:
    base_url:
      gemini: "https://x"
`,
			want: "not supported",
		},
		{
			name: "bad proxy",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    proxy: "ftp://proxy"
`,
			want: "proxy",
		},
		{
			name: "bool max retries",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    max_retries: true
`,
			want: "max_retries",
		},
		{
			name: "too many retries",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    max_retries: 11
`,
			want: "max_retries",
		},
		{
			name: "nan timeout",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    timeout: .nan
`,
			want: "timeout",
		},
		{
			name: "empty endpoints",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    endpoints:
      openai: {}
`,
			want: "non-empty mapping",
		},
		{
			name: "empty endpoint table",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    endpoints: {}
`,
			want: "at least one endpoint",
		},
		{
			name: "unsupported endpoint",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    endpoints:
      openai:
        foo: "/foo"
`,
			want: "not a supported endpoint",
		},
		{
			name: "removed completions alias",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    endpoints:
      openai:
        completions: "/completions"
`,
			want: "not a supported endpoint",
		},
		{
			name: "endpoint protocol whitespace",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    endpoints:
      openai:
        chat_completions: "/chat/completions"
      " openai ":
        responses: "/responses"
`,
			want: "leading or trailing whitespace",
		},
		{
			name: "base url protocol whitespace",
			content: `
providers:
  p:
    base_url:
      " openai ": "https://x"
`,
			want: "leading or trailing whitespace",
		},
		{
			name: "relative endpoint path",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    endpoints:
      openai:
        chat_completions: "chat/completions"
`,
			want: "path starting with",
		},
		{
			name: "endpoint path with query",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    endpoints:
      openai:
        chat_completions: "/chat/completions?x=1"
`,
			want: "without a scheme, host, query, or fragment",
		},
		{
			name: "endpoint path with whitespace",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    endpoints:
      openai:
        chat_completions: "/chat completions"
`,
			want: "whitespace",
		},
		{
			name: "endpoint protocol mismatch",
			content: `
providers:
  p:
    base_url:
      anthropic: "https://x"
    endpoints:
      anthropic:
        chat_completions: "/chat/completions"
`,
			want: "only supported for protocol",
		},
		{
			name: "endpoint without base url",
			content: `
providers:
  p:
    base_url:
      anthropic: "https://x"
    endpoints:
      openai:
        chat_completions: "/chat/completions"
`,
			want: "requires base_url.openai",
		},
		{
			name: "missing base url",
			content: `
providers:
  p:
    api_key: "k"
`,
			want: "at least one protocol",
		},
		{
			name: "unknown provider reference",
			content: `
models:
  m:
    providers:
      - provider_id: "missing"
        model_id: "x"
`,
			want: "unknown provider",
		},
		{
			name: "no providers",
			content: `
models:
  m:
    providers: []
`,
			want: "non-empty array",
		},
		{
			name: "unknown provider keys",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
models:
  m:
    providers:
      - provider_id: "p"
        model_id: "x"
        extra: 1
`,
			want: "extra",
		},
		{
			name: "negative weight",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
models:
  m:
    providers:
      - provider_id: "p"
        model_id: "x"
        weight: -0.5
`,
			want: "positive number",
		},
		{
			name: "negative max_retry_after",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
models:
  m:
    providers:
      - provider_id: "p"
        model_id: "x"
        max_retry_after: -1
`,
			want: "max_retry_after must be a non-negative number",
		},
		{
			name: "partial weights",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
  q:
    base_url:
      openai: "https://y"
models:
  m:
    providers:
      - provider_id: "p"
        model_id: "x"
        weight: 0.5
      - provider_id: "q"
        model_id: "y"
`,
			want: "weight for every provider",
		},
		{
			name: "weights not one",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
  q:
    base_url:
      openai: "https://y"
models:
  m:
    providers:
      - provider_id: "p"
        model_id: "x"
        weight: 0.4
      - provider_id: "q"
        model_id: "y"
        weight: 0.4
`,
			want: "sum to 1.0",
		},
		{
			name: "duplicate api key",
			content: `
users:
  a:
    api_key: "k"
  b:
    api_key: "k"
`,
			want: "duplicate api_key",
		},
		{
			name: "unknown allowed model",
			content: `
users:
  a:
    api_key: "k"
    allowed_models: ["missing"]
`,
			want: "unknown models",
		},
		{
			name: "bad port",
			content: `
server:
  port: 70000
`,
			want: "server.port",
		},
		{
			name: "zero port",
			content: `
server:
  port: 0
`,
			want: "server.port",
		},
		{
			name: "unsupported base url",
			content: `
providers:
  p:
    base_url:
      openai: "ftp://example.test"
`,
			want: "base_url.openai must be an http(s) URL",
		},
		{
			name: "timeout overflow",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    timeout: 1e300
`,
			want: "timeout is too large",
		},
		{
			name: "timeout above duration range",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    timeout: 9223372037
`,
			want: "timeout is too large",
		},
		{
			name: "unknown key",
			content: `
providers:
  p:
    base_url:
      openai: "https://x"
    protcol: "openai"
`,
			want: "unknown configuration keys",
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

	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfigEmptyFile(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"empty":        "",
		"whitespace":   "  \n\t\n",
		"comment only": "# nothing here\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, content))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "invalid YAML") {
				t.Fatalf("empty config must not be reported as a YAML error: %v", err)
			}
			if !strings.Contains(err.Error(), "empty") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadConfigRejectsMultipleDocuments(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig(writeConfig(t, `
server:
  port: 5000
---
server:
  port: 6000
`))
	if err == nil || !strings.Contains(err.Error(), "single YAML document") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadConfigUnknownKeysAreSorted(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig(writeConfig(t, `
server:
  zebra: 1
  alpha: 2
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "unknown configuration keys") ||
		strings.Index(err.Error(), "alpha") > strings.Index(err.Error(), "zebra") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadConfigAcceptsSocksProxy(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig(writeConfig(t, `
providers:
  p:
    base_url:
      openai: "https://x"
    proxy: "socks5://user:pass@127.0.0.1:1080"
`))
	if err != nil {
		t.Fatalf("socks5 proxy rejected: %v", err)
	}
}

func TestOutboundFor(t *testing.T) {
	t.Parallel()

	provider := &Provider{
		BaseURLs: map[ProviderProtocol]string{
			ProtocolOpenAI:    "https://openai.test",
			ProtocolAnthropic: "https://anthropic.test",
		},
		Endpoints: map[ProviderProtocol][]ProviderEndpoint{
			ProtocolOpenAI: {
				{Name: "chat_completions", Path: "/chat/completions", Wire: wireOpenAI},
				{Name: "responses", Path: "/responses", Wire: wireResponses},
			},
			ProtocolAnthropic: {
				{Name: "messages", Path: "/messages", Wire: wireAnthropic},
			},
		},
	}

	cases := []struct {
		name       string
		preferred  WireFormat
		protocols  []ProviderProtocol
		wantPath   string
		wantFormat WireFormat
		wantProto  ProviderProtocol
	}{
		{"openai preferred", "openai", nil, "/chat/completions", "openai", ProtocolOpenAI},
		{"responses preferred", "responses", nil, "/responses", "responses", ProtocolOpenAI},
		{"anthropic preferred", "anthropic", nil, "/messages", "anthropic", ProtocolAnthropic},
		{"openai-only fallback", "anthropic", []ProviderProtocol{ProtocolOpenAI}, "/chat/completions", "openai", ProtocolOpenAI},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			endpoint, protocol, ok := provider.OutboundFor(test.preferred, test.protocols...)
			if !ok || endpoint.Path != test.wantPath || endpoint.Wire != test.wantFormat || protocol != test.wantProto {
				t.Fatalf("OutboundFor = %+v, %q, %v", endpoint, protocol, ok)
			}
		})
	}
}

func TestRepositoryConfigLoads(t *testing.T) {
	t.Parallel()

	path := "config.yaml"
	if _, err := os.Stat(path); err != nil {
		path = "config.example.yaml"
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

	if path != "config.example.yaml" {
		return
	}
	overridden, err := LoadConfigWith(path, Overrides{Host: "127.0.0.1", Port: 4100, LogLevel: "debug"})
	if err != nil {
		t.Fatalf("load with overrides: %v", err)
	}
	if overridden.Host != "127.0.0.1" || overridden.Port != 4100 || overridden.LogLevel != "debug" {
		t.Fatalf("overrides not applied: %+v", overridden)
	}
	if cfg.Port != 4000 || cfg.LogLevel != "info" {
		t.Fatalf("base config changed: %+v", cfg)
	}
}

func TestLoadConfigAcceptsLargestSafeTimeout(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(writeConfig(t, `
providers:
  p:
    base_url:
      openai: "https://x"
    timeout: 9223372036
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
